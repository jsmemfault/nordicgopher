// Package tree writes the on-disk content tree that the Gopher server serves.
//
// Keeping generation and serving separate means the server only ever reads a
// directory: an ingester that fails leaves the previous copy in place, the
// whole mirror is diffable between runs, and the tree can be handed to
// Gophernicus instead if you would rather not run a custom daemon.
package tree

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"nordicgopher/internal/gopher"
	"nordicgopher/internal/text"
)

// Tree is a content tree rooted at a directory.
type Tree struct {
	Root string
}

func New(root string) *Tree { return &Tree{Root: root} }

func (t *Tree) path(selector string) string {
	rel := strings.TrimPrefix(filepath.ToSlash(selector), "/")
	return filepath.Join(t.Root, filepath.FromSlash(rel))
}

// MkdirAll creates a directory for a menu selector.
func (t *Tree) MkdirAll(selector string) error {
	return os.MkdirAll(t.path(selector), 0o755)
}

// WriteMenu writes a menu as the gophermap of the directory at selector.
func (t *Tree) WriteMenu(selector string, m gopher.Menu) error {
	if err := t.MkdirAll(selector); err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(t.path(selector), "gophermap"))
	if err != nil {
		return err
	}
	defer f.Close()
	return gopher.FormatGophermap(f, m)
}

// WriteFile writes a leaf file at selector, creating parent directories.
func (t *Tree) WriteFile(selector, content string) error {
	p := t.path(selector)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(content), 0o644)
}

// Create opens a leaf file for writing, creating parent directories. The
// caller closes it. Used for content too large to hold in memory.
func (t *Tree) Create(selector string) (*os.File, error) {
	p := t.path(selector)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, err
	}
	return os.Create(p)
}

// Swap atomically replaces dst with the tree, keeping the previous copy as
// dst+".prev" so a bad ingest run can be rolled back by hand.
func (t *Tree) Swap(dst string) error {
	prev := dst + ".prev"
	os.RemoveAll(prev)
	if _, err := os.Stat(dst); err == nil {
		if err := os.Rename(dst, prev); err != nil {
			return err
		}
	}
	return os.Rename(t.Root, dst)
}

// Attribution is the provenance block appended to every mirrored page.
//
// Each page says where its content came from and when it was fetched. That
// is not a disclaimer but a useful fact: the mirror is a dated snapshot, so
// a reader needs to know how old the copy is and where the live version
// lives. Retrofitting this later is awkward; it costs nothing now.
func Attribution(canonical string, retrieved time.Time) []string {
	return []string{
		"",
		text.Rule("-", text.Width),
		"Mirrored from: " + canonical,
		"Retrieved:     " + retrieved.UTC().Format("2006-01-02 15:04 MST"),
		"The canonical source above is authoritative; this copy may be stale",
		"or incompletely rendered.",
	}
}

// Page assembles a type-0 text page with a heading and an attribution footer.
//
// The heading is omitted when the body already opens with one, which is the
// usual case for a converted README: repeating it just pushes the content
// down the screen.
func Page(title, body, canonical string, retrieved time.Time) string {
	var b strings.Builder
	if !opensWithHeading(body) {
		b.WriteString(strings.ToUpper(title) + "\n")
		b.WriteString(text.Rule("=", min(len(title), text.Width)) + "\n\n")
	}
	b.WriteString(strings.TrimRight(body, "\n") + "\n")
	for _, l := range Attribution(canonical, retrieved) {
		b.WriteString(l + "\n")
	}
	return b.String()
}

// Header returns the standard menu banner: a title, a rule and a blank line.
func Header(title, subtitle string) gopher.Menu {
	var m gopher.Menu
	m.Add(gopher.Info(strings.ToUpper(title)))
	m.Add(gopher.Info(text.Rule("=", min(len(title), text.Width))))
	if subtitle != "" {
		for _, l := range text.Wrap(subtitle, text.Width, "") {
			m.Add(gopher.Info(l))
		}
	}
	m.Add(gopher.Blank())
	return m
}

// Footer returns the standard menu footer with a link home.
func Footer(retrieved time.Time) gopher.Menu {
	var m gopher.Menu
	m.Add(gopher.Blank())
	m.Add(gopher.Info(text.Rule("-", text.Width)))
	m.Add(gopher.Info(fmt.Sprintf("Generated %s by nordicgopher",
		retrieved.UTC().Format("2006-01-02 15:04 MST"))))
	m.Add(gopher.Link(gopher.TypeMenu, "Back to top", "/"))
	return m
}

// opensWithHeading reports whether the first two lines of body are a title
// and its underline, as both converters emit for a top-level section.
func opensWithHeading(body string) bool {
	lines := strings.SplitN(strings.TrimLeft(body, "\n"), "\n", 3)
	if len(lines) < 2 || strings.TrimSpace(lines[0]) == "" {
		return false
	}
	u := strings.TrimSpace(lines[1])
	if len(u) < 3 {
		return false
	}
	return strings.Trim(u, "=") == "" || strings.Trim(u, "-") == ""
}
