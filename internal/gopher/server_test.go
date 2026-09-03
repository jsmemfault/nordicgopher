package gopher

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "gophermap"), "banner\n1Docs\t/docs/\n0About\t/about.txt\n")
	mustWrite(t, filepath.Join(root, "about.txt"), "about this\n")
	mustWrite(t, filepath.Join(root, "docs", "note.txt"), "a note\n")
	mustWrite(t, filepath.Join(root, "docs", "blob.bin"), "\x00\x01\x02binary")
	return root
}

func mustWrite(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fetch(t *testing.T, s *Server, selector string) string {
	t.Helper()
	var buf bytes.Buffer
	if err := s.route(&buf, selector, ""); err != nil {
		t.Logf("route(%q) returned %v", selector, err)
	}
	return buf.String()
}

func TestServeMenuFromGophermap(t *testing.T) {
	s := &Server{Root: newTree(t), Host: "h", Port: 70}
	got := fetch(t, s, "/")
	if !strings.Contains(got, "1Docs\t/docs/\th\t70\r\n") {
		t.Errorf("menu item not rendered with server host:\n%q", got)
	}
	if !strings.HasSuffix(got, ".\r\n") {
		t.Errorf("menu not dot-terminated:\n%q", got)
	}
}

func TestTypeForFileDefaultsToBinary(t *testing.T) {
	// A mirrored Unix executable has no extension. Guessing text would
	// corrupt it, so an unknown extension must fall to binary.
	for name, want := range map[string]Type{
		"index.txt":   TypeText,
		"nrfutil":     TypeBinary,
		"tool.exe":    TypeBinary,
		"sdk.zip":     TypeArchive,
		"diagram.png": TypeImage,
	} {
		if got := TypeForFile(name); got != want {
			t.Errorf("TypeForFile(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestServeSynthesisedListing(t *testing.T) {
	// /docs has no gophermap, so the server generates one; a partially built
	// tree must still be navigable.
	s := &Server{Root: newTree(t), Host: "h", Port: 70}
	got := fetch(t, s, "/docs/")
	if !strings.Contains(got, "0note.txt\t/docs/note.txt") {
		t.Errorf("text file missing or mistyped:\n%s", got)
	}
	if !strings.Contains(got, "9blob.bin\t/docs/blob.bin") {
		t.Errorf("binary file should be type 9:\n%s", got)
	}
}

func TestServeTextIsDotTerminated(t *testing.T) {
	s := &Server{Root: newTree(t), Host: "h", Port: 70}
	if got := fetch(t, s, "/about.txt"); got != "about this\r\n.\r\n" {
		t.Errorf("got %q", got)
	}
}

func TestServeBinaryIsRaw(t *testing.T) {
	s := &Server{Root: newTree(t), Host: "h", Port: 70}
	if got := fetch(t, s, "/docs/blob.bin"); got != "\x00\x01\x02binary" {
		t.Errorf("binary item must not be rewritten or terminated: %q", got)
	}
}

func TestResolveRefusesEscapingRoot(t *testing.T) {
	s := &Server{Root: newTree(t)}
	for _, bad := range []string{"/../etc/passwd", "/docs/../../../../etc/passwd"} {
		// path.Clean collapses these to a path inside the root or to "/", so
		// the escape must be rejected or neutralised, never followed.
		got, err := s.resolve(bad)
		if err != nil {
			continue
		}
		abs, _ := filepath.Abs(s.Root)
		if !strings.HasPrefix(got, abs) {
			t.Errorf("resolve(%q) escaped the root: %q", bad, got)
		}
	}
}

func TestMissingSelectorReturnsError(t *testing.T) {
	s := &Server{Root: newTree(t), Host: "h", Port: 70}
	if got := fetch(t, s, "/nope"); !strings.HasPrefix(got, "3") {
		t.Errorf("expected a type-3 error item, got %q", got)
	}
}

func TestHandlerPrefixLongestWins(t *testing.T) {
	s := &Server{Root: newTree(t), Host: "h", Port: 70}
	s.Handle("/a/", func(w io.Writer, sel, q string) error {
		_, err := w.Write([]byte("short"))
		return err
	})
	s.Handle("/a/b/", func(w io.Writer, sel, q string) error {
		_, err := w.Write([]byte("long"))
		return err
	})
	if got := fetch(t, s, "/a/b/c"); got != "long" {
		t.Errorf("longest prefix should win, got %q", got)
	}
	if got := fetch(t, s, "/a/x"); got != "short" {
		t.Errorf("got %q", got)
	}
}

func TestHandlerReceivesQuery(t *testing.T) {
	s := &Server{Root: newTree(t), Host: "h", Port: 70}
	var seen string
	s.Handle("/search", func(w io.Writer, sel, q string) error {
		seen = q
		return nil
	})
	var buf bytes.Buffer
	s.route(&buf, "/search", "nrf9160 modem")
	if seen != "nrf9160 modem" {
		t.Errorf("query not passed through: %q", seen)
	}
}
