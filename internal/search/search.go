// Package search backs the Gopher type-7 item on the top-level menu.
//
// The corpus is loaded into memory once and scanned per query. Re-reading the
// tree from disk on every query cost seconds once the documentation ingest
// took the corpus past ten megabytes, which is too slow to sit behind a menu
// item; holding it in memory costs a few tens of megabytes and answers in
// milliseconds. It is still a scan, not an index -- the intended replacement
// is an SQLite FTS5 index built during ingest, which is why the query
// interface here is deliberately narrow.
package search

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"nordicgopher/internal/gopher"
	"nordicgopher/internal/text"
)

// MaxFileSize caps how much of a document is held, so one large file cannot
// dominate memory or query time.
const MaxFileSize = 1 << 20

// Index searches a content tree.
type Index struct {
	Root string

	mu      sync.RWMutex
	docs    []document
	stamp   time.Time // modification time of the tree's root menu when loaded
	loadErr error
}

// document is one searchable page, kept with a lowercased copy of its text so
// that a query does not have to allocate one per document.
type document struct {
	selector string
	title    string
	body     string
	lower    string
	lowTitle string
}

type hit struct {
	doc     *document
	context string
	score   int
}

// Query returns a menu of documents matching every term in q, best first.
func (ix *Index) Query(q string, limit int) (gopher.Menu, error) {
	terms := terms(q)
	if len(terms) == 0 {
		var m gopher.Menu
		m.Add(gopher.Info("Enter one or more words to search for."))
		m.Add(gopher.Link(gopher.TypeMenu, "Back to top", "/"))
		return m, nil
	}

	docs, err := ix.corpus()
	if err != nil {
		return nil, err
	}

	var hits []hit
	for i := range docs {
		if h, ok := match(&docs[i], terms); ok {
			hits = append(hits, h)
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	total := len(hits)
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}

	var out gopher.Menu
	out.Add(gopher.Info(fmt.Sprintf("Search: %s", q)))
	out.Add(gopher.Info(text.Rule("=", text.Width)))
	switch {
	case total == 0:
		out.Add(gopher.Info("No documents matched."))
	case total > len(hits):
		out.Add(gopher.Info(fmt.Sprintf("%d matching documents; showing the best %d.", total, len(hits))))
	default:
		out.Add(gopher.Info(fmt.Sprintf("%d matching documents, best first.", total)))
	}
	out.Add(gopher.Blank())

	for _, h := range hits {
		out.Add(gopher.Link(gopher.TypeText, text.Truncate(h.doc.title, gopher.MenuWidth), h.doc.selector))
		// One line only: the snippet comes from an already-wrapped document,
		// so re-wrapping it just strands a word or two on a second line.
		if h.context != "" {
			out.Add(gopher.Info("      " + text.Truncate(h.context, gopher.MenuWidth-6)))
		}
		out.Add(gopher.Blank())
	}
	out.Add(gopher.Link(gopher.TypeMenu, "Back to top", "/"))
	return out, nil
}

// Reload discards the in-memory corpus so the next query re-reads the tree.
func (ix *Index) Reload() {
	ix.mu.Lock()
	ix.docs, ix.stamp, ix.loadErr = nil, time.Time{}, nil
	ix.mu.Unlock()
}

// corpus returns the loaded documents, reading the tree if it has not been
// read or if the ingester has swapped a new one into place.
func (ix *Index) corpus() ([]document, error) {
	stamp := ix.treeStamp()

	ix.mu.RLock()
	if ix.docs != nil && stamp.Equal(ix.stamp) {
		docs, err := ix.docs, ix.loadErr
		ix.mu.RUnlock()
		return docs, err
	}
	ix.mu.RUnlock()

	ix.mu.Lock()
	defer ix.mu.Unlock()
	// Another query may have loaded it while this one waited.
	if ix.docs != nil && stamp.Equal(ix.stamp) {
		return ix.docs, ix.loadErr
	}
	docs, err := ix.load()
	ix.docs, ix.stamp, ix.loadErr = docs, stamp, err
	return docs, err
}

// treeStamp is the modification time of the tree's root menu. ngingest writes
// a whole tree and renames it into place, so this changes exactly when the
// content does.
func (ix *Index) treeStamp() time.Time {
	fi, err := os.Stat(filepath.Join(ix.Root, "gophermap"))
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

func (ix *Index) load() ([]document, error) {
	var docs []document
	err := filepath.WalkDir(ix.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".txt") {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > MaxFileSize {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(ix.Root, p)
		if err != nil {
			return nil
		}
		body := string(b)
		title := ""
		if i := strings.IndexByte(body, '\n'); i >= 0 {
			title = strings.TrimSpace(body[:i])
		}
		docs = append(docs, document{
			selector: "/" + filepath.ToSlash(rel),
			title:    title,
			body:     body,
			lower:    strings.ToLower(body),
			lowTitle: strings.ToLower(title),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].selector < docs[j].selector })
	return docs, nil
}

// match scores a document, requiring every term to appear. Occurrences in the
// title count extra: a document about a term beats one that mentions it.
func match(d *document, terms []string) (hit, bool) {
	score := 0
	for _, t := range terms {
		n := strings.Count(d.lower, t)
		if n == 0 {
			return hit{}, false
		}
		score += n
		if strings.Contains(d.lowTitle, t) {
			score += 25
		}
	}
	return hit{doc: d, context: context(d.body, terms), score: score}, true
}

// context returns a snippet for the result: the first matching line of actual
// prose. Headings, their underlines and the converter's footnote list all
// match terms readily but say nothing useful in a result list, so a line has
// to look like a sentence to be chosen. If none does, the first match is used
// rather than showing nothing.
func context(body string, terms []string) string {
	fallback := ""
	for i, l := range strings.Split(body, "\n") {
		if i == 0 {
			continue
		}
		ll := strings.ToLower(l)
		matched := false
		for _, t := range terms {
			if strings.Contains(ll, t) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		trimmed := strings.TrimSpace(l)
		if fallback == "" {
			fallback = trimmed
		}
		if isProse(trimmed) {
			return trimmed
		}
	}
	return fallback
}

// isProse rejects rules, headings and footnote entries.
func isProse(l string) bool {
	if len(l) < 20 || len(strings.Fields(l)) < 5 {
		return false
	}
	if strings.HasPrefix(l, "[") || strings.HasPrefix(l, "Mirrored from:") {
		return false
	}
	if strings.Trim(l, "=-_") == "" {
		return false
	}
	// An all-caps line is a converted top-level heading.
	return l != strings.ToUpper(l)
}

func terms(q string) []string {
	var out []string
	for _, f := range strings.Fields(strings.ToLower(q)) {
		f = strings.Trim(f, `.,;:!?"'()[]`)
		if len(f) >= 2 {
			out = append(out, f)
		}
	}
	return out
}
