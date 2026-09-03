package index

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, root, "ncs/nrf54l/index.txt",
		"WORKING WITH NRF54L15\n=====================\n\nThe nRF54L15 uses the SB_CONFIG_NETCORE_EMPTY Kconfig option.\n")
	write(t, root, "ncs/mesh/index.txt",
		"BLUETOOTH MESH\n==============\n\nMesh provisioning is described here, and mentions nrf54l15 once.\n")
	write(t, root, "ncs/other/index.txt",
		"UNRELATED PAGE\n==============\n\nNothing here matches the interesting terms at all.\n")
	// Server metadata must not be searchable.
	write(t, root, "caps.txt", "CAPS\nServerSoftware=nordicgopher\n")
	write(t, root, "robots.txt", "User-agent: *\nDisallow: /ncs/\n")
	return root
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

func build(t *testing.T, prefix string) *Index {
	t.Helper()
	ix, err := Build(newTree(t), prefix)
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

func selectors(rs []Result) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Doc.Selector)
	}
	return out
}

func TestBuildRecordsPrefixedSelectors(t *testing.T) {
	ix := build(t, "/nrf")
	for _, d := range ix.Docs {
		if !strings.HasPrefix(d.Selector, "/nrf/") {
			t.Errorf("selector missing prefix: %q", d.Selector)
		}
	}
	if len(ix.Docs) != 3 {
		t.Errorf("caps.txt and robots.txt are metadata, not content: got %d docs", len(ix.Docs))
	}
}

func TestQueryRequiresEveryTerm(t *testing.T) {
	ix := build(t, "")
	if got := selectors(ix.Query([]string{"mesh", "provisioning"}, 10)); len(got) != 1 {
		t.Errorf("both terms present in one doc only: %v", got)
	}
	if got := ix.Query([]string{"mesh", "unrelated"}, 10); len(got) != 0 {
		t.Errorf("terms in different documents must not match: %v", selectors(got))
	}
	if got := ix.Query([]string{"mesh", "nonexistentterm"}, 10); len(got) != 0 {
		t.Errorf("an unmatched term must exclude everything: %v", selectors(got))
	}
}

func TestQueryMatchesByPrefix(t *testing.T) {
	// This is what replaces the scan's substring matching: a partial
	// identifier has to find the full one, since that is how people search
	// for a part number they half-remember.
	ix := build(t, "")
	if got := ix.Query([]string{"nrf54"}, 10); len(got) != 2 {
		t.Errorf("nrf54 should prefix-match nrf54l15 in both docs: %v", selectors(got))
	}
	if got := ix.Query([]string{"nrf54l15"}, 10); len(got) != 2 {
		t.Errorf("exact token: %v", selectors(got))
	}
	// A prefix match is not a substring match: the interior of a word is
	// deliberately not searchable.
	if got := ix.Query([]string{"54l15"}, 10); len(got) != 0 {
		t.Errorf("interior matching is not supported: %v", selectors(got))
	}
}

func TestQueryRanksTitleMatchesFirst(t *testing.T) {
	ix := build(t, "")
	got := selectors(ix.Query([]string{"nrf54l15"}, 10))
	if len(got) != 2 {
		t.Fatalf("got %v", got)
	}
	// Both mention it; only one is about it.
	if !strings.Contains(got[0], "nrf54l") {
		t.Errorf("title match should rank first: %v", got)
	}
}

func TestTokenizeSplitsIdentifiers(t *testing.T) {
	got := map[string]bool{}
	for _, tok := range Tokenize("The SB_CONFIG_NETCORE_EMPTY option") {
		got[tok] = true
	}
	// Whole symbol, so it can be searched by its real name...
	if !got["sb_config_netcore_empty"] {
		t.Error("whole identifier missing")
	}
	// ...and its parts, because that is how they are remembered.
	for _, part := range []string{"config", "netcore", "empty"} {
		if !got[part] {
			t.Errorf("component %q missing", part)
		}
	}
	if got["t"] {
		t.Error("single characters should not be indexed")
	}
}

func TestQueryNCountsTotalAndLimits(t *testing.T) {
	ix := build(t, "")
	hits, total := ix.QueryN([]string{"nrf54"}, 1)
	if total != 2 {
		t.Errorf("total should count every match, got %d", total)
	}
	if len(hits) != 1 {
		t.Errorf("limit should apply to the returned slice, got %d", len(hits))
	}
}

func TestWriteLoadRoundTrip(t *testing.T) {
	ix := build(t, "/nrf")
	path := filepath.Join(t.TempDir(), "search.idx")
	if err := ix.WriteFile(path); err != nil {
		t.Fatal(err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Docs) != len(ix.Docs) || got.Terms() != ix.Terms() {
		t.Fatalf("docs %d/%d terms %d/%d",
			len(got.Docs), len(ix.Docs), got.Terms(), ix.Terms())
	}
	before := selectors(ix.Query([]string{"nrf54", "kconfig"}, 10))
	after := selectors(got.Query([]string{"nrf54", "kconfig"}, 10))
	if len(before) != len(after) {
		t.Fatalf("query results differ after reload: %v vs %v", before, after)
	}
	for i := range before {
		if before[i] != after[i] {
			t.Errorf("result %d: %q vs %q", i, before[i], after[i])
		}
	}
}

func TestLoadRejectsForeignFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not.idx")
	if err := os.WriteFile(path, []byte("this is not an index"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("expected a magic-number error")
	}
}
