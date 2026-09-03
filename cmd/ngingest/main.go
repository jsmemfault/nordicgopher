// Command ngingest builds the content tree that nordicgopher serves.
//
// It generates into a staging directory and swaps the result into place, so a
// failed or partial run never replaces a working mirror. The previous tree is
// kept alongside as <dir>.prev for a manual rollback.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"nordicgopher/internal/gopher"
	"nordicgopher/internal/httpcache"
	"nordicgopher/internal/index"
	"nordicgopher/internal/ingest/artifactory"
	"nordicgopher/internal/ingest/github"
	"nordicgopher/internal/ingest/ncsdocs"
	"nordicgopher/internal/text"
	"nordicgopher/internal/tree"
)

func main() {
	out := flag.String("out", "content", "content tree directory to produce")
	cache := flag.String("cache", ".cache", "HTTP cache directory")
	catalog := flag.String("catalog", "data/artifacts.json", "curated download catalogue")
	orgs := flag.String("orgs", "NordicSemiconductor,nrfconnect", "GitHub organisations to mirror")
	maxRepos := flag.Int("max-repos", 15, "repositories per organisation (0 = all)")
	maxReleases := flag.Int("max-releases", 8, "release notes per repository")
	only := flag.String("only", "", "run a single ingester: github, files or ncsdocs")
	docsRef := flag.String("docs-ref", "main", "sdk-nrf branch or tag to read documentation from")
	maxDocs := flag.Int("max-docs", 0, "documentation pages to mirror (0 = all reachable)")
	workers := flag.Int("workers", 8, "concurrent source fetches for documentation")
	rawGap := flag.Duration("raw-gap", 25*time.Millisecond,
		"minimum interval between CDN source fetches; raise it if the host gets throttled")
	retries := flag.Int("retries", httpcache.DefaultRetries,
		"retries with backoff for a throttled or failed fetch")
	mirrorMax := flag.Int64("mirror-max-bytes", 64<<20,
		"largest artifact copied into the tree; larger ones stay proxied (0 = never copy)")
	admin := flag.String("admin", "", "administrator contact published in caps.txt")
	ngsearchPath := flag.String("ngsearch", "/usr/local/bin/ngsearch",
		"path to the ngsearch binary, written into the generated CGI wrapper")
	searchCGI := flag.String("search-cgi", "",
		"filename of the search CGI to point the type-7 item at, e.g. search.cgi; "+
			"empty uses this server's built-in /search handler")
	prefix := flag.String("selector-prefix", "",
		"prefix for every generated selector, e.g. /nrf when the tree is served as a "+
			"subdirectory of an existing gopherhole")
	host := flag.String("host", "", "public hostname, published in caps.txt")
	verbose := flag.Bool("v", false, "verbose logging")
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	cfg := options{
		out: *out, cache: *cache, catalog: *catalog, orgs: *orgs, only: *only,
		maxRepos: *maxRepos, maxReleases: *maxReleases,
		docsRef: *docsRef, maxDocs: *maxDocs, workers: *workers,
		mirrorMax: *mirrorMax, admin: *admin, host: *host, prefix: *prefix,
		searchCGI: *searchCGI, ngsearchPath: *ngsearchPath,
		rawGap: *rawGap, retries: *retries,
	}
	if err := run(log, cfg); err != nil {
		log.Error("ingest failed", "err", err)
		os.Exit(1)
	}
}

// options carries the command line, so adding an ingester does not mean
// threading another parameter through run.
type options struct {
	out, cache, catalog, orgs, only string
	maxRepos, maxReleases           int
	docsRef                         string
	maxDocs, workers                int
	mirrorMax                       int64
	admin, host, prefix, searchCGI  string
	ngsearchPath                    string
	rawGap                          time.Duration
	retries                         int
}

func run(log *slog.Logger, opt options) error {
	now := time.Now()
	out, cache, only := opt.out, opt.cache, opt.only

	// Staging must share a parent with the destination so the swap is an
	// atomic rename rather than a copy.
	parent := filepath.Dir(absOr(out))
	cleanStaging(log, parent)
	staging, err := os.MkdirTemp(parent, ".ngingest-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)

	// A prefix lets the tree be dropped into an existing gopherhole as a
	// subdirectory. Selectors are absolute on the wire, so every one of them
	// carries it, while the directory layout does not: the tree is
	// self-contained and gets installed at <server root>/<prefix>.
	prefix := strings.TrimSuffix(opt.prefix, "/")
	if prefix != "" && !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	if prefix != "" {
		log.Info("generating with a selector prefix", "prefix", prefix)
	}

	t := tree.New(staging)
	t.Prefix = prefix

	// Two clients over one cache directory. The API client is throttled
	// because its budget is counted in requests per hour; the CDN client is
	// paced only lightly, since its limit is about rate rather than volume.
	// The CDN gap is not zero on purpose: a datacenter address fetching 889
	// files as fast as it can is exactly the shape that gets throttled,
	// where the same run from a home connection never is.
	hc := httpcache.New(cache)
	hc.Log = log
	hc.Retries = opt.retries
	raw := httpcache.New(cache)
	raw.Log = log
	raw.MinGap = opt.rawGap
	raw.Retries = opt.retries

	var root gopher.Menu
	root.Add(banner()...)

	if only == "" || only == "github" {
		ing := &github.Ingester{
			HTTP: hc,
			Log:  log,
			Cfg: github.Config{
				Root:        prefix + "/github",
				Orgs:        splitList(opt.orgs),
				MaxRepos:    opt.maxRepos,
				MaxReleases: opt.maxReleases,
				Token:       firstEnv("GITHUB_TOKEN", "GH_TOKEN"),
			},
		}
		frag, err := ing.Run(t, now)
		if err != nil {
			return fmt.Errorf("github: %w", err)
		}
		root.Add(frag...)
	}

	if only == "" || only == "ncsdocs" {
		ing := &ncsdocs.Ingester{
			HTTP: hc,
			Raw:  raw,
			Log:  log,
			Cfg: ncsdocs.Config{
				Repo:      "nrfconnect/sdk-nrf",
				Ref:       opt.docsRef,
				DocRoot:   "doc/nrf",
				Root:      prefix + "/ncs",
				Entry:     "index",
				Shortcuts: "shortcuts.txt",
				Links:     "links.txt",
				MaxDocs:   opt.maxDocs,
				Workers:   opt.workers,
				Token:     firstEnv("GITHUB_TOKEN", "GH_TOKEN"),
			},
		}
		frag, err := ing.Run(t, now)
		if err != nil {
			return fmt.Errorf("ncsdocs: %w", err)
		}
		root.Add(frag...)
	}

	if only == "" || only == "files" {
		ing := &artifactory.Ingester{
			HTTP: hc,
			Log:  log,
			Cfg: artifactory.Config{
				Root:           prefix + "/files",
				CatalogPath:    opt.catalog,
				Verify:         true,
				MirrorMaxBytes: opt.mirrorMax,
			},
		}
		frag, err := ing.Run(t, now)
		if err != nil {
			return fmt.Errorf("artifactory: %w", err)
		}
		root.Add(frag...)
	}

	root.Add(gopher.Blank())
	root.Add(gopher.Link(gopher.TypeSearch, "Search this mirror", searchSelector(opt, prefix)))
	root.Add(gopher.Link(gopher.TypeText, "About this mirror, and what it does not include", prefix+"/about.txt"))
	root.Add(gopher.Blank())
	root.Add(gopher.Info(text.Rule("-", text.Width)))
	root.Add(gopher.Info("Generated " + now.UTC().Format("2006-01-02 15:04 MST") +
		" - a plain-text mirror; canonical sources are authoritative"))

	if err := t.WriteMenu("/", root); err != nil {
		return err
	}
	if err := t.WriteFile("/about.txt", about(now)); err != nil {
		return err
	}
	// caps.txt and robots.txt are fetched from a hole's root by directories
	// and crawlers, so they belong at the served root. When this tree is a
	// subdirectory of somebody else's hole, that root is theirs, not ours.
	if prefix == "" {
		if err := t.WriteFile("/caps.txt", caps(opt, now)); err != nil {
			return err
		}
		if err := t.WriteFile("/robots.txt", robots()); err != nil {
			return err
		}
	} else {
		log.Info("skipping caps.txt and robots.txt: they belong at the root of the hosting gopherhole, not in a subdirectory")
	}

	if opt.searchCGI != "" {
		if err := writeSearchCGI(t, opt, prefix, absOr(out)); err != nil {
			return fmt.Errorf("writing search CGI: %w", err)
		}
		log.Info("search CGI written", "selector", searchSelector(opt, prefix))
	}

	// The index is built from the finished tree, so it must be written
	// before the swap but after every ingester has run.
	ix, err := index.Build(staging, prefix)
	if err != nil {
		return fmt.Errorf("building search index: %w", err)
	}
	if err := ix.WriteFile(filepath.Join(staging, "search.idx")); err != nil {
		return fmt.Errorf("writing search index: %w", err)
	}
	log.Info("search index built", "documents", len(ix.Docs), "terms", ix.Terms())

	if err := t.Swap(absOr(out)); err != nil {
		return err
	}
	log.Info("content tree written", "dir", out, "api", hc.Stats(), "raw", raw.Stats())
	return nil
}

func banner() gopher.Menu {
	var m gopher.Menu
	for _, l := range []string{
		"",
		"  nordicgopher",
		"  " + text.Rule("=", 60),
		"",
		"  A plain-text mirror of publicly available Nordic Semiconductor",
		"  resources, served over Gopher (RFC 1436).",
		"",
	} {
		m.Add(gopher.Info(l))
	}
	return m
}

func about(now time.Time) string {
	body := `WHAT THIS IS
============

A plain-text mirror of publicly available Nordic Semiconductor material,
served over Gopher. Every page carries the URL it was mirrored from and the
time it was fetched, because the mirror is a dated snapshot: the canonical
source is always authoritative, and a page here may be stale.

WHAT IS INCLUDED
================

  * The public GitHub organisations: repository metadata, README files
    and release notes, converted from Markdown and reStructuredText to
    fixed-width text.

  * The public file server at files.nordicsemi.com: a curated set of
    downloadable artifacts, streamed through this server as Gopher
    binary items, plus the repository index.

WHAT IS NOT INCLUDED, AND WHY
=============================

The main web properties - www.nordicsemi.com, docs.nordicsemi.com,
devzone.nordicsemi.com, academy.nordicsemi.com and infocenter - sit
behind a Cloudflare managed challenge that returns HTTP 403 to any
non-browser client. That applies to robots.txt and sitemap.xml as well,
so there is no documented crawl policy to follow and no polite-crawler
path to take. Mirroring those sources needs either an allowlisted
egress address or a content feed arranged with their owners, not a
workaround; until then they are absent rather than half-scraped.

Nothing behind authentication is mirrored, by design.

HOW TO NAVIGATE
===============

Menus are directories; text items are documents. Links inside a
document cannot be selectable, because a Gopher text file has no inline
link concept, so they appear as numbered markers with the targets
collected under "Links" at the end of the document.

The search item on the top-level menu matches words against every
mirrored document.
`
	return tree.Page("About this mirror", body, "https://www.nordicsemi.com/", now)
}

// cleanStaging removes staging directories left behind by a run that was
// killed before its own cleanup could happen. Without this they accumulate,
// each holding a full copy of the tree.
func cleanStaging(log *slog.Logger, parent string) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), ".ngingest-") {
			p := filepath.Join(parent, e.Name())
			log.Info("removing abandoned staging directory", "dir", p)
			os.RemoveAll(p)
		}
	}
}

// caps returns the server capabilities file. Gopher directories and
// aggregators fetch /caps.txt to learn who runs a hole and how its selectors
// are shaped; publishing it is how a new hole becomes findable and how a
// reader knows who to contact.
func caps(opt options, now time.Time) string {
	host := opt.host
	if host == "" {
		host = "localhost"
	}
	admin := opt.admin
	if admin == "" {
		admin = "unset - pass -admin to ngingest"
	}
	return strings.Join([]string{
		"CAPS",
		"",
		"##",
		"## This is a gopher server capabilities file, as fetched by gopher",
		"## directories and aggregators.",
		"##",
		"",
		"caps.serial=" + now.UTC().Format("20060102150405"),
		"caps.vendor=nordicgopher",
		"caps.version=1",
		"",
		"expire.capabilities=86400",
		"expire.software=604800",
		"",
		"ServerSoftware=nordicgopher",
		"ServerSoftwareVersion=0.2",
		"ServerArchitecture=" + runtime.GOOS + "-" + runtime.GOARCH,
		"ServerDescription=Plain-text mirror of public Nordic Semiconductor resources",
		"ServerAdmin=" + admin,
		"",
		"## Selectors are slash-separated paths. Menus end in a slash;",
		"## documents end in .txt.",
		"PathDelimeter=/",
		"PathIdentity=.",
		"PathParent=..",
		"PathParentDouble=FALSE",
		"PathKeepPreDelimeter=FALSE",
		"ServerDefaultEncoding=utf-8",
		"",
	}, "\n")
}

// robots returns a robots.txt for gopher crawlers, which do fetch it.
//
// The mirror is someone else's content republished, so the canonical pages
// should win a search rather than these copies. Crawling the documentation
// tree is 733 pages of duplicate text; the entry points are enough for a hole
// to be discoverable.
func robots() string {
	return strings.Join([]string{
		"# nordicgopher - a plain-text mirror",
		"#",
		"# This hole republishes public Nordic Semiconductor material. The",
		"# canonical sources are authoritative and should rank ahead of these",
		"# copies, so indexing is limited to the entry points.",
		"",
		"User-agent: *",
		"Crawl-delay: 30",
		"Disallow: /ncs/",
		"Disallow: /github/",
		"Disallow: /files/artifacts/",
		"Disallow: /dl/",
		"Disallow: /search",
		"Allow: /",
		"",
	}, "\n")
}

// writeSearchCGI writes the wrapper that the hosting daemon executes.
//
// It lives inside the content tree because its selector must, and the tree is
// replaced wholesale on every ingest -- so a hand-placed script would be
// deleted by the next run. Generating it also means the paths it passes are
// always the ones this run produced.
func writeSearchCGI(t *tree.Tree, opt options, prefix, served string) error {
	name := strings.TrimPrefix(opt.searchCGI, "/")
	script := fmt.Sprintf(`#!/bin/sh
# Generated by ngingest -- do not edit; the next ingest overwrites it.
#
# Motsognir executes a selector ending in .cgi and copies stdout to the
# client, passing the type-7 terms in QUERY_STRING_SEARCH.
exec %s     -index %s/search.idx     -root %s     -prefix %s
`, shellQuote(opt.ngsearchPath), shellQuote(served), shellQuote(served), shellQuote(prefix))

	if err := t.WriteFile(prefix+"/"+name, script); err != nil {
		return err
	}
	// It is executed, not read.
	return os.Chmod(filepath.Join(t.Root, name), 0o755)
}

// shellQuote makes a path safe inside the generated script.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'''`) + "'"
}

// searchSelector points the type-7 item at whatever will answer queries: this
// server's built-in handler, or the CGI that Motsognir executes.
func searchSelector(opt options, prefix string) string {
	if opt.searchCGI != "" {
		return prefix + "/" + strings.TrimPrefix(opt.searchCGI, "/")
	}
	return prefix + "/search"
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func firstEnv(names ...string) string {
	for _, n := range names {
		if v := os.Getenv(n); v != "" {
			return v
		}
	}
	return ""
}

func absOr(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}
