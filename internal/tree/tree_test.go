package tree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nordicgopher/internal/gopher"
)

func TestPathIgnoresTheSelectorPrefix(t *testing.T) {
	// The prefix is a wire concept. Baking it into the layout would nest the
	// tree a second time when it is installed at <server root>/nrf, and
	// would put a root gophermap and an about.txt into the hosting hole's
	// own directory.
	tr := &Tree{Root: "/srv/content", Prefix: "/nrf"}
	for selector, want := range map[string]string{
		"/nrf":             "/srv/content",
		"/nrf/":            "/srv/content",
		"/nrf/about.txt":   "/srv/content/about.txt",
		"/nrf/ncs/app_dev": "/srv/content/ncs/app_dev",
		"/":                "/srv/content",
		"/about.txt":       "/srv/content/about.txt",
	} {
		if got := tr.path(selector); got != want {
			t.Errorf("path(%q) = %q, want %q", selector, got, want)
		}
	}
}

func TestPathWithoutPrefix(t *testing.T) {
	tr := &Tree{Root: "/srv/content"}
	if got := tr.path("/ncs/index.txt"); got != "/srv/content/ncs/index.txt" {
		t.Errorf("got %q", got)
	}
}

func TestWriteMenuStripsPrefixFromLayoutButNotFromItems(t *testing.T) {
	root := t.TempDir()
	tr := &Tree{Root: root, Prefix: "/nrf"}

	var m gopher.Menu
	m.Add(gopher.Link(gopher.TypeText, "About", "/nrf/about.txt"))
	if err := tr.WriteMenu("/nrf/ncs", m); err != nil {
		t.Fatal(err)
	}

	// Written where the prefix has been stripped...
	b, err := os.ReadFile(filepath.Join(root, "ncs", "gophermap"))
	if err != nil {
		t.Fatalf("menu not written at the stripped path: %v", err)
	}
	// ...but the selector inside still carries it, because that is what
	// goes on the wire.
	if !strings.Contains(string(b), "/nrf/about.txt") {
		t.Errorf("selector lost its prefix:\n%s", b)
	}
}

func TestPageOmitsARedundantHeading(t *testing.T) {
	body := "PYNRFJPROG\n==========\n\nProse.\n"
	got := Page("nordicsemiconductor/pynrfjprog - README.md", body, "https://example.com/", time.Now())
	if strings.Count(got, "PYNRFJPROG") != 1 {
		t.Errorf("heading duplicated:\n%s", got)
	}
	if !strings.Contains(got, "Mirrored from: https://example.com/") {
		t.Errorf("attribution missing:\n%s", got)
	}
}

func TestPageAddsAHeadingWhenTheBodyHasNone(t *testing.T) {
	got := Page("Some Title", "Just prose, no heading.\n", "https://example.com/", time.Now())
	if !strings.Contains(got, "SOME TITLE") {
		t.Errorf("heading not added:\n%s", got)
	}
}

func TestSwapKeepsThePreviousTree(t *testing.T) {
	base := t.TempDir()
	dst := filepath.Join(base, "content")

	// An existing tree that a failed run must not destroy.
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "marker"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	staging := filepath.Join(base, "staging")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "marker"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := (&Tree{Root: staging}).Swap(dst); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "marker")); string(b) != "new" {
		t.Errorf("new tree not in place, got %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dst+".prev", "marker")); string(b) != "old" {
		t.Errorf("previous tree not kept for rollback, got %q", b)
	}
}
