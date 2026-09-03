package gopher

import (
	"bytes"
	"strings"
	"testing"
)

func TestMenuWriteToWireFormat(t *testing.T) {
	m := Menu{
		Info("hello"),
		Link(TypeMenu, "Docs", "/docs/"),
		{Type: TypeText, Display: "Elsewhere", Selector: "/x", Host: "other.host", Port: 70},
	}
	var buf bytes.Buffer
	if err := m.WriteTo(&buf, "example.org", 7070); err != nil {
		t.Fatal(err)
	}
	want := "ihello\tfake\terror.host\t1\r\n" +
		"1Docs\t/docs/\texample.org\t7070\r\n" +
		"0Elsewhere\t/x\tother.host\t70\r\n" +
		".\r\n"
	if got := buf.String(); got != want {
		t.Errorf("wire format\n got %q\nwant %q", got, want)
	}
}

func TestMenuStripsFieldSeparators(t *testing.T) {
	// A display string containing a tab would silently shift every later
	// field, so the encoder has to neutralise it.
	m := Menu{Link(TypeMenu, "a\tb\r\nc", "sel\there")}
	var buf bytes.Buffer
	m.WriteTo(&buf, "h", 70)
	line := strings.SplitN(buf.String(), "\r\n", 2)[0]
	if n := strings.Count(line, "\t"); n != 3 {
		t.Errorf("expected exactly 3 field separators, got %d in %q", n, line)
	}
}

func TestWriteTextDotStuffing(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteText(&buf, strings.NewReader("ok\n.hidden\n..also\n")); err != nil {
		t.Fatal(err)
	}
	want := "ok\r\n..hidden\r\n...also\r\n.\r\n"
	if got := buf.String(); got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestParseGophermap(t *testing.T) {
	src := "# comment\n" +
		"plain info line\n" +
		"1Docs\t/docs/\n" +
		"0Far\t/far\tother.host\t7000\n"
	m, err := ParseGophermap(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 3 {
		t.Fatalf("got %d items, want 3: %+v", len(m), m)
	}
	if m[0].Type != TypeInfo || m[0].Display != "plain info line" {
		t.Errorf("info line: %+v", m[0])
	}
	if m[1].Type != TypeMenu || m[1].Selector != "/docs/" || m[1].Host != "" {
		t.Errorf("local item should inherit host: %+v", m[1])
	}
	if m[2].Host != "other.host" || m[2].Port != 7000 {
		t.Errorf("explicit host/port: %+v", m[2])
	}
}

func TestGophermapRoundTrip(t *testing.T) {
	in := Menu{Info("banner"), Link(TypeText, "About", "/about.txt")}
	var buf bytes.Buffer
	if err := FormatGophermap(&buf, in); err != nil {
		t.Fatal(err)
	}
	out, err := ParseGophermap(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(in) {
		t.Fatalf("round trip changed length: %d -> %d", len(in), len(out))
	}
	for i := range in {
		if in[i].Type != out[i].Type || in[i].Display != out[i].Display {
			t.Errorf("item %d: %+v -> %+v", i, in[i], out[i])
		}
		// An informational line's selector is a placeholder that clients
		// ignore, so it is not required to survive a round trip.
		if in[i].Type != TypeInfo && in[i].Selector != out[i].Selector {
			t.Errorf("item %d selector: %q -> %q", i, in[i].Selector, out[i].Selector)
		}
	}
}

// motsognirItemType reproduces how Motsognir reads a gophermap line: the
// first character of a non-empty line is the item type, and only a blank line
// is treated as informational. It does NOT infer the type from the absence of
// a tab, which Bucktooth and Gophernicus do.
//
// See explodegophermapline() in motsognir.c.
func motsognirItemType(line string) byte {
	if line == "" {
		return 'i'
	}
	return line[0]
}

func TestFormatIsMotsognirCompatible(t *testing.T) {
	// A generated tree has to be servable by the Motsognir instance already
	// running on the deployment host. Every line must therefore begin with a
	// real item type: a bare banner line would be served as an item of type
	// ' ' or '=' and render as garbage.
	m := Menu{
		Info(""),
		Info("  nordicgopher"),
		Info("  ============================================"),
		Info("----------------------------------------------"),
		Info("1024 bytes of prose that must not look like a menu item"),
		Link(TypeMenu, "Docs", "/nrf/ncs/"),
		Link(TypeText, "About", "/nrf/about.txt"),
		URL("Source", "https://example.com/"),
	}
	var buf bytes.Buffer
	if err := FormatGophermap(&buf, m); err != nil {
		t.Fatal(err)
	}

	valid := map[byte]bool{'i': true, '0': true, '1': true, '3': true,
		'5': true, '7': true, '9': true, 'g': true, 'I': true, 'h': true}
	for n, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		got := motsognirItemType(line)
		if !valid[got] {
			t.Errorf("line %d would be item type %q under Motsognir: %q", n, got, line)
		}
	}
}

func TestFormatInfoLinesCarryTheTypeAndATab(t *testing.T) {
	var buf bytes.Buffer
	FormatGophermap(&buf, Menu{Info("hello")})
	// The tab matters too: with none, a tab-inferring server would read the
	// whole line as a display string including the leading "i".
	if got := buf.String(); got != "ihello\t\n" {
		t.Errorf("got %q, want %q", got, "ihello\t\n")
	}
}

func TestProxyResolveRejectsEscapes(t *testing.T) {
	p := &Proxy{Allow: map[string]string{"/dl/": "https://files.example.com/artifactory/"}}

	got, err := p.resolve("/dl/repo/tool.bin")
	if err != nil || got != "https://files.example.com/artifactory/repo/tool.bin" {
		t.Fatalf("legitimate path: got %q err %v", got, err)
	}

	for _, bad := range []string{
		"/dl/../../etc/passwd",
		"/dl/%2e%2e/%2e%2e/etc/passwd",
		"/dl//evil.example.com/x",
		"/dl//",
		"/dl/",
		"/other/thing",
	} {
		if got, err := p.resolve(bad); err == nil {
			t.Errorf("resolve(%q) should have failed, got %q", bad, got)
		}
	}
}
