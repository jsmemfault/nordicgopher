package text

import (
	"strings"
	"testing"
)

func TestWrapHangIsAdditional(t *testing.T) {
	got := Wrap("  * a fairly long list item that will need to wrap somewhere", 30, "    ")
	if len(got) < 2 {
		t.Fatalf("expected a wrap, got %q", got)
	}
	if !strings.HasPrefix(got[0], "  * ") {
		t.Errorf("first line lost its indent: %q", got[0])
	}
	// Continuation = first-line indent (2) + hang (4).
	if !strings.HasPrefix(got[1], "      ") || strings.HasPrefix(got[1], "       ") {
		t.Errorf("continuation indent should be 6 columns, got %q", got[1])
	}
}

func TestWrapDoesNotBreakLongTokens(t *testing.T) {
	url := "https://docs.example.com/a/very/long/path/that/exceeds/the/width"
	for _, l := range Wrap("see "+url, 20, "") {
		if strings.Contains(l, "example.com") && !strings.Contains(l, url) {
			t.Errorf("URL was split across lines: %q", l)
		}
	}
}

func TestMarkdownHeadingsAndCode(t *testing.T) {
	out := Markdown("# Title\n\nSome prose.\n\n```c\nint main(void) { return 0; }\n```\n", 70)
	if !strings.Contains(out, "TITLE\n=====") {
		t.Errorf("missing underlined H1:\n%s", out)
	}
	if !strings.Contains(out, "    int main(void) { return 0; }") {
		t.Errorf("fenced code should be indented verbatim:\n%s", out)
	}
}

func TestMarkdownLinksBecomeFootnotes(t *testing.T) {
	out := Markdown("See [the docs](https://example.com/docs) and [again](https://example.com/docs).", 70)
	if !strings.Contains(out, "the docs [1]") {
		t.Errorf("missing footnote marker:\n%s", out)
	}
	if strings.Count(out, "[1] https://example.com/docs") != 1 {
		t.Errorf("repeated target should share one footnote:\n%s", out)
	}
	if strings.Contains(out, "[2]") {
		t.Errorf("duplicate target got a second number:\n%s", out)
	}
}

func TestMarkdownDropsBadges(t *testing.T) {
	out := Markdown("[ ![Build](https://img.shields.io/x.svg) ](https://ci.example.com)\n\n# Real Title\n", 70)
	if strings.Contains(out, "shields.io") || strings.Contains(out, "Build") {
		t.Errorf("badge survived conversion:\n%s", out)
	}
	if !strings.Contains(out, "REAL TITLE") {
		t.Errorf("content after badge was lost:\n%s", out)
	}
}

func TestMarkdownRelativeLinksResolved(t *testing.T) {
	out := MarkdownOpts("See [migration](MIGRATION.md).", Options{
		Width:   70,
		BaseURL: "https://github.com/org/repo/blob/main/README.md",
	})
	if !strings.Contains(out, "https://github.com/org/repo/blob/main/MIGRATION.md") {
		t.Errorf("relative link not resolved:\n%s", out)
	}
}

func TestMarkdownListContinuationIsNotCode(t *testing.T) {
	// Four-space indentation after a list item is a continuation, not a code
	// block; emitting it verbatim leaves markup in the output and overruns
	// the wrap width.
	out := Markdown("* first item\n    continued **here**\n", 70)
	if strings.Contains(out, "**") {
		t.Errorf("continuation was treated as code, markup survived:\n%s", out)
	}
}

func TestMarkdownGitHubAlert(t *testing.T) {
	out := Markdown("> [!NOTE]\n> Mind the gap.\n", 70)
	if !strings.Contains(out, "| NOTE") || !strings.Contains(out, "| Mind the gap.") {
		t.Errorf("alert not rendered as a labelled quote:\n%s", out)
	}
}

func TestRSTSectionHierarchy(t *testing.T) {
	src := `Top Level
#########

Prose here.

Second Level
============

More prose.
`
	out := RST(src, 70)
	if !strings.Contains(out, "TOP LEVEL\n=========") {
		t.Errorf("first adornment should be level 1:\n%s", out)
	}
	if !strings.Contains(out, "Second Level\n------------") {
		t.Errorf("second adornment should be level 2:\n%s", out)
	}
}

func TestRSTAdmonitionAndDroppedDirectives(t *testing.T) {
	src := `.. contents::
   :local:

.. note::
   Read this first.

.. code-block:: c

   int x = 1;
`
	out := RST(src, 70)
	if strings.Contains(out, "contents") || strings.Contains(out, ":local:") {
		t.Errorf("contents directive should be dropped:\n%s", out)
	}
	if !strings.Contains(out, "NOTE: Read this first.") {
		t.Errorf("note admonition not labelled:\n%s", out)
	}
	if !strings.Contains(out, "    int x = 1;") {
		t.Errorf("code-block body should be verbatim:\n%s", out)
	}
}

func TestRSTNamedLinkFootnote(t *testing.T) {
	out := RST("See the `SDK docs <https://example.com/sdk>`_ for details.\n", 70)
	if !strings.Contains(out, "SDK docs [1]") {
		t.Errorf("named link not footnoted:\n%s", out)
	}
	if !strings.Contains(out, "[1] https://example.com/sdk") {
		t.Errorf("footnote target missing:\n%s", out)
	}
}

func TestRSTRolesKeepTheirText(t *testing.T) {
	out := RST("Call :c:func:`nrf_write` before :ref:`init`.\n", 70)
	if !strings.Contains(out, "nrf_write") || !strings.Contains(out, "init") {
		t.Errorf("role content lost:\n%s", out)
	}
	if strings.Contains(out, ":c:func:") || strings.Contains(out, "`") {
		t.Errorf("role markup survived:\n%s", out)
	}
}

func TestAutoDispatchesByExtension(t *testing.T) {
	if !strings.Contains(Auto("x.rst", "Title\n=====\n", 70), "TITLE") {
		t.Error("rst not dispatched")
	}
	if !strings.Contains(Auto("x.md", "# Title\n", 70), "TITLE") {
		t.Error("markdown not dispatched")
	}
	if got := Auto("x.log", "already plain\n", 70); !strings.Contains(got, "already plain") {
		t.Errorf("plain fallback: %q", got)
	}
}

func TestConvertedOutputStaysWithinWidth(t *testing.T) {
	src := strings.Repeat("some reasonably long prose that has to be reflowed properly ", 20)
	for _, l := range strings.Split(Markdown(src, 70), "\n") {
		if len(l) > 70 {
			t.Errorf("line exceeds width (%d): %q", len(l), l)
		}
	}
}
