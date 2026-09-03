// Package ncsdocs ingests the nRF Connect SDK documentation from its
// reStructuredText source.
//
// The rendered documentation sits behind the Cloudflare challenge that blocks
// every nordicsemi.com host, but the source it is built from is open on
// GitHub. Reading the source is also simply better: the toctree directives
// give the documentation's own hierarchy, section adornments give real
// structure, and cross-reference labels can be resolved to selectors inside
// the mirror. None of that survives a pass over rendered HTML.
//
// Only one API request is needed -- the recursive git tree -- because the
// files themselves come from raw.githubusercontent.com, which is a CDN and
// not subject to the API rate limit. A full ingest therefore works without a
// token.
package ncsdocs

import (
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"nordicgopher/internal/gopher"
	"nordicgopher/internal/httpcache"
	"nordicgopher/internal/text"
	"nordicgopher/internal/tree"
)

// Config selects what to mirror.
type Config struct {
	Repo    string // "nrfconnect/sdk-nrf"
	Ref     string // branch or tag to read
	DocRoot string // "doc/nrf", the Sphinx source directory
	Root    string // selector prefix, e.g. "/ncs"
	Entry   string // root document name, e.g. "index"

	// Shortcuts and Links name the project's shared substitution and
	// hyperlink-target files, relative to DocRoot. The Sphinx configuration
	// appends both to every document through rst_epilog, so a document read
	// without them is missing words, not just links.
	Shortcuts string
	Links     string

	MaxDocs int // 0 = every reachable document
	Workers int // concurrent source fetches
	Token   string
}

// Ingester writes the documentation subtree.
type Ingester struct {
	// HTTP is used for the GitHub API and is throttled.
	HTTP *httpcache.Client
	// Raw fetches source files from the CDN and need not be throttled.
	Raw *httpcache.Client
	Cfg Config
	Log *slog.Logger

	// Populated during Run. The maps exist so that resolving a
	// cross-reference or a parent link is a lookup rather than a scan: with
	// 850 documents and thousands of references, scanning would dominate.
	sources    map[string]string
	bySelector map[string]*doc
	parent     map[string]*doc
	extraMu    sync.Mutex
}

// doc is one source document.
type doc struct {
	name     string // docname, e.g. "app_dev/create_application"
	path     string // repository path
	selector string // menu selector in the mirror
	title    string
	children []string // docnames, from toctree directives, in order
	labels   []string // cross-reference labels this document defines
	refs     []string // cross-reference labels this document uses
	src      string
}

func (in *Ingester) log() *slog.Logger {
	if in.Log != nil {
		return in.Log
	}
	return slog.Default()
}

func (in *Ingester) headers() http.Header {
	h := http.Header{}
	h.Set("Accept", "application/vnd.github+json")
	h.Set("X-GitHub-Api-Version", "2022-11-28")
	if in.Cfg.Token != "" {
		h.Set("Authorization", "Bearer "+in.Cfg.Token)
	}
	return h
}

// Run writes the subtree and returns a menu fragment for the site root.
func (in *Ingester) Run(t *tree.Tree, now time.Time) (gopher.Menu, error) {
	paths, err := in.listSource()
	if err != nil {
		return nil, fmt.Errorf("listing %s: %w", in.Cfg.Repo, err)
	}
	in.log().Info("documentation source listed",
		"repo", in.Cfg.Repo, "ref", in.Cfg.Ref, "files", len(paths))

	sources, err := in.fetchAll(paths)
	if err != nil {
		return nil, err
	}
	in.sources = sources

	subs := text.ParseSubstitutions(sources[path.Join(in.Cfg.DocRoot, in.Cfg.Shortcuts)])
	links := text.ParseLinkTargets(sources[path.Join(in.Cfg.DocRoot, in.Cfg.Links)])
	in.log().Info("resolution tables loaded",
		"substitutions", len(subs), "link targets", len(links))

	docs := in.parseDocs(sources, subs)
	in.index(docs)
	order := in.reachable(docs)
	if n := in.Cfg.MaxDocs; n > 0 && len(order) > n {
		in.log().Warn("truncating document set", "reachable", len(order), "limit", n)
		order = order[:n]
	}

	// Labels resolve to the text file of the document that defines them, so
	// a cross-reference in prose becomes a footnote pointing into the mirror.
	refTargets := map[string]string{}
	included := map[string]bool{}
	for _, name := range order {
		included[name] = true
	}
	for _, name := range order {
		d := docs[name]
		for _, l := range d.labels {
			if _, taken := refTargets[l]; !taken {
				refTargets[l] = d.selector + "index.txt"
			}
		}
	}

	written := 0
	for _, name := range order {
		if err := in.writeDoc(t, docs, docs[name], included, subs, links, refTargets, now); err != nil {
			in.log().Warn("skipping document", "doc", name, "err", err)
			continue
		}
		written++
	}

	in.log().Info("ncs docs ingest complete",
		"documents", written, "labels", len(refTargets), "cache", in.Raw.Stats())

	var frag gopher.Menu
	frag.Add(gopher.Link(gopher.TypeMenu,
		fmt.Sprintf("nRF Connect SDK documentation - %d pages (%s)", written, in.Cfg.Ref),
		in.Cfg.Root+"/"))
	return frag, nil
}

// --- source discovery ------------------------------------------------------

type treeResponse struct {
	Truncated bool `json:"truncated"`
	Tree      []struct {
		Path string `json:"path"`
		Type string `json:"type"`
		Size int64  `json:"size"`
	} `json:"tree"`
}

// listSource returns the documentation source files, in one API request.
func (in *Ingester) listSource() ([]string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/git/trees/%s?recursive=1",
		in.Cfg.Repo, in.Cfg.Ref)
	var resp treeResponse
	if err := in.HTTP.GetJSON(url, in.headers(), &resp); err != nil {
		return nil, err
	}
	if resp.Truncated {
		// The API caps a recursive tree; a truncated response would silently
		// omit documents, which is worse than failing.
		return nil, fmt.Errorf("git tree response was truncated; ingest would be incomplete")
	}

	prefix := in.Cfg.DocRoot + "/"
	var out []string
	for _, e := range resp.Tree {
		if e.Type != "blob" || !strings.HasPrefix(e.Path, prefix) {
			continue
		}
		// .rst files are documents; .txt files are the shared includes and
		// the substitution and link tables.
		if strings.HasSuffix(e.Path, ".rst") || strings.HasSuffix(e.Path, ".txt") {
			out = append(out, e.Path)
		}
	}
	sort.Strings(out)
	return out, nil
}

// fetchAll retrieves every source file concurrently from the CDN.
func (in *Ingester) fetchAll(paths []string) (map[string]string, error) {
	workers := in.Cfg.Workers
	if workers <= 0 {
		workers = 8
	}

	var (
		mu      sync.Mutex
		out     = make(map[string]string, len(paths))
		failed  int
		jobs    = make(chan string)
		wg      sync.WaitGroup
		started = time.Now()
	)

	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				url := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s",
					in.Cfg.Repo, in.Cfg.Ref, p)
				b, err := in.Raw.Get(url, nil)
				mu.Lock()
				if err != nil {
					failed++
					in.log().Debug("source fetch failed", "path", p, "err", err)
				} else {
					out[p] = string(b)
				}
				mu.Unlock()
			}
		}()
	}
	for _, p := range paths {
		jobs <- p
	}
	close(jobs)
	wg.Wait()

	in.log().Info("documentation source fetched",
		"files", len(out), "failed", failed, "dur", time.Since(started).Round(time.Second))
	if len(out) == 0 {
		return nil, fmt.Errorf("no source files could be fetched")
	}
	return out, nil
}

// --- document graph --------------------------------------------------------

func (in *Ingester) parseDocs(sources map[string]string, subs map[string]string) map[string]*doc {
	docs := map[string]*doc{}
	for p, src := range sources {
		if !strings.HasSuffix(p, ".rst") {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(p, in.Cfg.DocRoot+"/"), ".rst")
		entries, glob := text.ParseToctree(src)
		d := &doc{
			name:     name,
			path:     p,
			selector: in.selectorFor(name),
			title:    text.ExpandSubstitutions(text.Title(src), subs),
			labels:   text.ParseLabels(src),
			refs:     text.ParseRefs(src),
			src:      src,
		}
		if d.title == "" {
			d.title = path.Base(name)
		}
		d.children = in.resolveToctree(name, entries, glob, sources)
		docs[name] = d
	}
	return docs
}

// resolveToctree turns toctree entries into docnames. An entry is relative to
// the directory of the document containing it, or absolute when it starts
// with a slash, matching Sphinx.
func (in *Ingester) resolveToctree(from string, entries []string, glob bool, sources map[string]string) []string {
	dir := path.Dir(from)
	if dir == "." {
		dir = ""
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e, "/") {
			e = strings.TrimPrefix(e, "/")
		} else if dir != "" {
			e = path.Join(dir, e)
		}
		if glob && strings.Contains(e, "*") {
			out = append(out, in.expandGlob(e, sources)...)
			continue
		}
		if _, ok := sources[path.Join(in.Cfg.DocRoot, e+".rst")]; ok {
			out = append(out, e)
		}
	}
	return out
}

// expandGlob resolves a globbed toctree entry. Sphinx globs are shell-like
// over docnames; only "*" appears in practice.
func (in *Ingester) expandGlob(pattern string, sources map[string]string) []string {
	re, err := regexp.Compile("^" + strings.ReplaceAll(regexp.QuoteMeta(pattern), `\*`, "[^/]*") + "$")
	if err != nil {
		return nil
	}
	var out []string
	prefix := in.Cfg.DocRoot + "/"
	for p := range sources {
		if !strings.HasSuffix(p, ".rst") {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(p, prefix), ".rst")
		if re.MatchString(name) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// reachable returns the documents reachable from the entry point, in
// depth-first toctree order. Documents outside the graph are orphans that the
// published documentation does not link either, so they are left out.
func (in *Ingester) reachable(docs map[string]*doc) []string {
	seen := map[string]bool{}
	var order []string
	var walk func(name string)
	walk = func(name string) {
		if seen[name] {
			return
		}
		d, ok := docs[name]
		if !ok {
			return
		}
		seen[name] = true
		order = append(order, name)
		for _, c := range d.children {
			walk(c)
		}
	}
	walk(in.Cfg.Entry)

	if orphans := len(docs) - len(order); orphans > 0 {
		in.log().Info("documents not reachable from the entry point were skipped",
			"orphans", orphans, "reachable", len(order))
	}
	return order
}

// selectorFor maps a docname to a menu selector. A directory's "index"
// document becomes the directory itself, so /ncs/libraries/ is the libraries
// index rather than /ncs/libraries/index/.
func (in *Ingester) selectorFor(name string) string {
	name = strings.TrimSuffix(name, "/index")
	if name == in.Cfg.Entry || name == "" {
		return in.Cfg.Root + "/"
	}
	var parts []string
	for _, p := range strings.Split(name, "/") {
		parts = append(parts, slug(p))
	}
	return in.Cfg.Root + "/" + strings.Join(parts, "/") + "/"
}

// --- output ----------------------------------------------------------------

func (in *Ingester) writeDoc(t *tree.Tree, docs map[string]*doc, d *doc,
	included map[string]bool, subs, links, refTargets map[string]string, now time.Time) error {

	in.log().Debug("converting document", "doc", d.name)
	canonical := fmt.Sprintf("https://github.com/%s/blob/%s/%s", in.Cfg.Repo, in.Cfg.Ref, d.path)

	body := text.RSTOpts(d.src, text.Options{
		Width:         text.Width,
		BaseURL:       canonical,
		Substitutions: subs,
		LinkTargets:   links,
		RefTargets:    refTargets,
		Include:       in.includer(d),
	})
	if err := t.WriteFile(d.selector+"index.txt", tree.Page(d.title, body, canonical, now)); err != nil {
		return err
	}

	m := tree.Header(d.title, "")
	m.Add(gopher.Link(gopher.TypeText, "Read this page", d.selector+"index.txt"))
	m.Add(gopher.Blank())

	if len(d.children) > 0 {
		m.Add(gopher.Info("Subpages"))
		m.Add(gopher.Info(text.Rule("-", 8)))
		for _, c := range d.children {
			child, ok := docs[c]
			if !ok || !included[c] {
				continue
			}
			m.Add(gopher.Link(gopher.TypeMenu,
				text.Truncate(child.title, gopher.MenuWidth), child.selector))
		}
		m.Add(gopher.Blank())
	}

	// Cross-references. In a type-0 text file these can only be footnotes, so
	// the menu is where they become followable.
	var seen = map[string]bool{d.selector: true}
	var xrefs gopher.Menu
	for _, label := range d.refs {
		sel, ok := refTargets[label]
		if !ok {
			continue
		}
		dir := strings.TrimSuffix(sel, "index.txt")
		if seen[dir] {
			continue
		}
		seen[dir] = true
		target := in.bySelector[dir]
		if target == nil {
			continue
		}
		xrefs.Add(gopher.Link(gopher.TypeMenu,
			text.Truncate(target.title, gopher.MenuWidth), dir))
	}
	if len(xrefs) > 0 {
		m.Add(gopher.Info("Pages referenced from this one"))
		m.Add(gopher.Info(text.Rule("-", 29)))
		m.Add(xrefs...)
		m.Add(gopher.Blank())
	}

	m.Add(gopher.Info("  Source: " + d.path))
	if parent := in.parent[d.name]; parent != nil {
		m.Add(gopher.Link(gopher.TypeMenu, "Up to "+text.Truncate(parent.title, 60), parent.selector))
	}
	m.Add(gopher.URL("This page's source on the web", canonical))
	m.Add(tree.Footer(now)...)
	return t.WriteMenu(strings.TrimSuffix(d.selector, "/"), m)
}

// includer resolves ".. include::" paths for one document. A leading slash is
// relative to the Sphinx source directory; anything else is relative to the
// including document.
func (in *Ingester) includer(d *doc) func(string) (string, bool) {
	return func(p string) (string, bool) {
		var full string
		if strings.HasPrefix(p, "/") {
			full = path.Join(in.Cfg.DocRoot, strings.TrimPrefix(p, "/"))
		} else {
			full = path.Join(path.Dir(d.path), p)
		}
		if src, ok := in.sources[full]; ok {
			if src == "" {
				return "", false
			}
			return src, true
		}
		// Some includes reach outside the Sphinx source directory into the
		// wider repository (a script's own documentation, for instance).
		// Those are not in the listed source set, so they are fetched on
		// demand and remembered. Paths that leave the repository entirely --
		// Zephyr's tree, reached through Sphinx's include mapping -- cannot
		// be resolved here and are reported in the output instead.
		if src, ok := in.fetchExtra(full); ok {
			return src, true
		}
		return "", false
	}
}

// fetchExtra retrieves a repository file outside the listed source set.
func (in *Ingester) fetchExtra(p string) (string, bool) {
	if strings.HasPrefix(p, "..") || p == "" {
		return "", false
	}
	in.extraMu.Lock()
	defer in.extraMu.Unlock()
	if src, ok := in.sources[p]; ok {
		return src, ok
	}
	url := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s", in.Cfg.Repo, in.Cfg.Ref, p)
	b, err := in.Raw.Get(url, nil)
	if err != nil {
		in.log().Debug("out-of-tree include unavailable", "path", p, "err", err)
		in.sources[p] = "" // negative cache; do not ask again
		return "", false
	}
	in.sources[p] = string(b)
	return string(b), true
}

// index builds the selector and parent lookups.
func (in *Ingester) index(docs map[string]*doc) {
	in.bySelector = make(map[string]*doc, len(docs))
	in.parent = make(map[string]*doc, len(docs))
	for _, d := range docs {
		in.bySelector[d.selector] = d
	}
	// A document can appear in more than one toctree; the first wins, which
	// matches the order the entry point is walked in.
	names := make([]string, 0, len(docs))
	for name := range docs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for _, c := range docs[name].children {
			if _, taken := in.parent[c]; !taken && c != name {
				in.parent[c] = docs[name]
			}
		}
	}
}

var reSlug = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func slug(s string) string {
	s = reSlug.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-.")
	if s == "" {
		return "item"
	}
	return s
}
