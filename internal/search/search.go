// Package search backs the Gopher type-7 item on the top-level menu.
//
// It scans the generated text files on each query. That is adequate for a
// phase-one tree of a few thousand documents and keeps the server
// dependency-free; the intended replacement is an SQLite FTS5 index built by
// the ingester, which is why the query interface here is deliberately narrow.
package search

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"nordicgopher/internal/gopher"
	"nordicgopher/internal/text"
)

// MaxFileSize caps how much of a document is scanned, so one large file
// cannot dominate query latency.
const MaxFileSize = 1 << 20

// Index searches a content tree.
type Index struct {
	Root string
}

type hit struct {
	selector string
	title    string
	context  string
	score    int
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

	var hits []hit
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
		h, ok := match(string(b), terms)
		if !ok {
			return nil
		}
		rel, err := filepath.Rel(ix.Root, p)
		if err != nil {
			return nil
		}
		h.selector = "/" + filepath.ToSlash(rel)
		hits = append(hits, h)
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}

	var out gopher.Menu
	out.Add(gopher.Info(fmt.Sprintf("Search: %s", q)))
	out.Add(gopher.Info(text.Rule("=", text.Width)))
	if len(hits) == 0 {
		out.Add(gopher.Info("No documents matched."))
	} else {
		out.Add(gopher.Info(fmt.Sprintf("%d matching documents, best first.", len(hits))))
	}
	out.Add(gopher.Blank())

	for _, h := range hits {
		out.Add(gopher.Link(gopher.TypeText, text.Truncate(h.title, gopher.MenuWidth), h.selector))
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

// match scores a document, requiring every term to appear. Occurrences in the
// title line count extra: a document about a term beats one that mentions it.
func match(body string, terms []string) (hit, bool) {
	lower := strings.ToLower(body)
	lines := strings.Split(body, "\n")
	title := ""
	if len(lines) > 0 {
		title = strings.TrimSpace(lines[0])
	}
	lowerTitle := strings.ToLower(title)

	score := 0
	for _, t := range terms {
		n := strings.Count(lower, t)
		if n == 0 {
			return hit{}, false
		}
		score += n
		if strings.Contains(lowerTitle, t) {
			score += 25
		}
	}

	return hit{title: title, context: context(lines, terms), score: score}, true
}

// context returns a snippet for the result: the first matching line of actual
// prose. Headings, their underlines and the converter's footnote list all
// match terms readily but say nothing useful in a result list, so a line has
// to look like a sentence to be chosen. If none does, the first match is used
// rather than showing nothing.
func context(lines []string, terms []string) string {
	fallback := ""
	for i, l := range lines {
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
