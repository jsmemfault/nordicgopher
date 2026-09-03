package ncsdocs

import (
	"testing"
)

func newIngester() *Ingester {
	return &Ingester{Cfg: Config{
		Repo: "nrfconnect/sdk-nrf", Ref: "main",
		DocRoot: "doc/nrf", Root: "/ncs", Entry: "index",
	}}
}

func TestSelectorFor(t *testing.T) {
	in := newIngester()
	cases := map[string]string{
		"index":                      "/ncs/",
		"app_dev":                    "/ncs/app_dev/",
		"app_dev/create_application": "/ncs/app_dev/create_application/",
		// A directory's index document is the directory itself, so the
		// libraries index is not /ncs/libraries/index/.
		"libraries/index":           "/ncs/libraries/",
		"protocols/bluetooth/index": "/ncs/protocols/bluetooth/",
	}
	for name, want := range cases {
		if got := in.selectorFor(name); got != want {
			t.Errorf("selectorFor(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestResolveToctreeIsRelativeToTheDocument(t *testing.T) {
	in := newIngester()
	sources := map[string]string{
		"doc/nrf/app_dev/create_application.rst": "",
		"doc/nrf/gsg_guides.rst":                 "",
		"doc/nrf/libraries/networking/coap.rst":  "",
	}

	// An entry in doc/nrf/app_dev.rst resolves against doc/nrf/.
	got := in.resolveToctree("app_dev", []string{"app_dev/create_application"}, false, sources)
	if len(got) != 1 || got[0] != "app_dev/create_application" {
		t.Errorf("relative entry: %v", got)
	}

	// An entry in doc/nrf/libraries/index.rst resolves against
	// doc/nrf/libraries/.
	got = in.resolveToctree("libraries/index", []string{"networking/coap"}, false, sources)
	if len(got) != 1 || got[0] != "libraries/networking/coap" {
		t.Errorf("nested relative entry: %v", got)
	}

	// A leading slash is absolute from the source root.
	got = in.resolveToctree("libraries/index", []string{"/gsg_guides"}, false, sources)
	if len(got) != 1 || got[0] != "gsg_guides" {
		t.Errorf("absolute entry: %v", got)
	}
}

func TestResolveToctreeDropsMissingDocuments(t *testing.T) {
	in := newIngester()
	sources := map[string]string{"doc/nrf/real.rst": ""}
	if got := in.resolveToctree("index", []string{"real", "imaginary"}, false, sources); len(got) != 1 {
		t.Errorf("a toctree entry with no source file must be dropped: %v", got)
	}
}

func TestExpandGlob(t *testing.T) {
	in := newIngester()
	sources := map[string]string{
		"doc/nrf/samples/a.rst":      "",
		"doc/nrf/samples/b.rst":      "",
		"doc/nrf/samples/deep/c.rst": "",
		"doc/nrf/other.rst":          "",
	}
	got := in.resolveToctree("index", []string{"samples/*"}, true, sources)
	if len(got) != 2 {
		// "*" does not cross a directory separator.
		t.Fatalf("got %v, want the two direct children", got)
	}
	if got[0] != "samples/a" || got[1] != "samples/b" {
		t.Errorf("got %v", got)
	}
}

func TestReachableFollowsToctreeOrder(t *testing.T) {
	in := newIngester()
	docs := map[string]*doc{
		"index":  {name: "index", children: []string{"b", "a"}},
		"a":      {name: "a"},
		"b":      {name: "b", children: []string{"b1"}},
		"b1":     {name: "b1"},
		"orphan": {name: "orphan"},
	}
	got := in.reachable(docs)
	want := []string{"index", "b", "b1", "a"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("position %d: got %q want %q (full %v)", i, got[i], want[i], got)
		}
	}
}

func TestReachableSurvivesCycles(t *testing.T) {
	in := newIngester()
	docs := map[string]*doc{
		"index": {name: "index", children: []string{"a"}},
		"a":     {name: "a", children: []string{"b"}},
		"b":     {name: "b", children: []string{"a", "index"}},
	}
	got := in.reachable(docs)
	if len(got) != 3 {
		t.Errorf("a toctree cycle must not repeat documents: %v", got)
	}
}

func TestIndexBuildsParentLinks(t *testing.T) {
	in := newIngester()
	docs := map[string]*doc{
		"index": {name: "index", selector: "/ncs/", children: []string{"a"}},
		"a":     {name: "a", selector: "/ncs/a/", children: []string{"a/b"}},
		"a/b":   {name: "a/b", selector: "/ncs/a/b/"},
	}
	in.index(docs)

	if in.parent["a/b"] == nil || in.parent["a/b"].name != "a" {
		t.Errorf("parent of a/b: %+v", in.parent["a/b"])
	}
	if in.parent["index"] != nil {
		t.Errorf("the entry point has no parent: %+v", in.parent["index"])
	}
	if in.bySelector["/ncs/a/b/"] == nil || in.bySelector["/ncs/a/b/"].name != "a/b" {
		t.Errorf("selector lookup failed")
	}
}

func TestSlugKeepsSelectorsSafe(t *testing.T) {
	for in, want := range map[string]string{
		"create_application": "create_application",
		"nrf54l":             "nrf54l",
		"a b/c":              "a-b-c",
		"..":                 "item",
	} {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}
