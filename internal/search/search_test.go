package search

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nordicgopher/internal/gopher"
)

func newIndex(t *testing.T) *Index {
	t.Helper()
	root := t.TempDir()
	write(t, root, "a.txt", "NRF9160 MODEM GUIDE\n===================\n\nThe modem firmware is delivered as a zip archive for the device.\n")
	write(t, root, "sub/b.txt", "BLUETOOTH MESH\n==============\n\nMesh provisioning uses the modem only incidentally in this example.\n")
	write(t, root, "sub/gophermap", "iignore me\tfake\terror.host\t1\n")
	return &Index{Root: root}
}

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func selectors(m gopher.Menu) []string {
	var out []string
	for _, it := range m {
		if it.Type == gopher.TypeText {
			out = append(out, it.Selector)
		}
	}
	return out
}

func TestQueryRequiresAllTerms(t *testing.T) {
	ix := newIndex(t)
	m, err := ix.Query("modem zip", 10)
	if err != nil {
		t.Fatal(err)
	}
	got := selectors(m)
	if len(got) != 1 || got[0] != "/a.txt" {
		t.Errorf("expected only /a.txt, got %v", got)
	}
}

func TestQueryRanksTitleMatchesFirst(t *testing.T) {
	ix := newIndex(t)
	m, err := ix.Query("modem", 10)
	if err != nil {
		t.Fatal(err)
	}
	got := selectors(m)
	if len(got) != 2 {
		t.Fatalf("expected both documents, got %v", got)
	}
	// Both mention "modem"; only a.txt is about it.
	if got[0] != "/a.txt" {
		t.Errorf("title match should rank first, got %v", got)
	}
}

func TestQuerySkipsGophermaps(t *testing.T) {
	ix := newIndex(t)
	m, _ := ix.Query("ignore", 10)
	if s := selectors(m); len(s) != 0 {
		t.Errorf("menu files should not be searchable, got %v", s)
	}
}

func TestQueryEmptyIsGuidance(t *testing.T) {
	ix := newIndex(t)
	m, err := ix.Query("   ", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(selectors(m)) != 0 {
		t.Error("empty query should return no documents")
	}
}

func TestSnippetPrefersProse(t *testing.T) {
	ix := newIndex(t)
	m, _ := ix.Query("modem", 10)
	var infos []string
	for _, it := range m {
		if it.Type == gopher.TypeInfo {
			infos = append(infos, it.Display)
		}
	}
	joined := strings.Join(infos, "\n")
	if !strings.Contains(joined, "modem firmware is delivered") {
		t.Errorf("snippet should be a prose line, not a heading:\n%s", joined)
	}
}
