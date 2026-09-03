package text

import (
	"strings"
	"testing"
	"time"
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
	if !strings.Contains(out, "NOTE:") || !strings.Contains(out, "| Read this first.") {
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

func TestRSTSubstitutionsExpand(t *testing.T) {
	out := RSTOpts("The |NCS| is a kit.\n", Options{
		Width:         70,
		Substitutions: map[string]string{"NCS": "nRF Connect SDK"},
	})
	if !strings.Contains(out, "The nRF Connect SDK is a kit.") {
		t.Errorf("substitution not expanded:\n%s", out)
	}
}

func TestRSTUnknownSubstitutionIsKept(t *testing.T) {
	// Deleting it would silently corrupt the sentence; leaving it visible
	// signals a missing definition.
	out := RSTOpts("The |MYSTERY| is here.\n", Options{
		Width:         70,
		Substitutions: map[string]string{"NCS": "nRF Connect SDK"},
	})
	if !strings.Contains(out, "|MYSTERY|") {
		t.Errorf("unknown substitution should survive:\n%s", out)
	}
}

func TestRSTRefResolvesToMirrorSelector(t *testing.T) {
	out := RSTOpts("See :ref:`the guide <app_dev>` for details.\n", Options{
		Width:      70,
		BaseURL:    "https://github.com/org/repo/blob/main/doc/x.rst",
		RefTargets: map[string]string{"app_dev": "/ncs/app_dev/index.txt"},
	})
	if !strings.Contains(out, "the guide [1]") {
		t.Errorf("ref text missing:\n%s", out)
	}
	// The selector must not be rewritten against the upstream base URL.
	if !strings.Contains(out, "[1] /ncs/app_dev/index.txt") {
		t.Errorf("ref should point into the mirror:\n%s", out)
	}
}

func TestRSTRefWithUnknownLabelDropsTheLabel(t *testing.T) {
	out := RST("See :ref:`the guide <nowhere>` for details.\n", 70)
	if strings.Contains(out, "nowhere") || strings.Contains(out, "<") {
		t.Errorf("unresolved label should not be printed:\n%s", out)
	}
	if !strings.Contains(out, "the guide") {
		t.Errorf("display text lost:\n%s", out)
	}
}

func TestRSTNamedLinkTable(t *testing.T) {
	out := RSTOpts("Read the `Zephyr`_ docs.\n", Options{
		Width:       70,
		LinkTargets: map[string]string{"Zephyr": "https://zephyrproject.org/"},
	})
	if !strings.Contains(out, "Zephyr [1]") || !strings.Contains(out, "[1] https://zephyrproject.org/") {
		t.Errorf("named reference not resolved:\n%s", out)
	}
}

func TestRSTIncludeExpands(t *testing.T) {
	out := RSTOpts(".. include:: /shared.txt\n", Options{
		Width: 70,
		Include: func(p string) (string, bool) {
			if p == "/shared.txt" {
				return "Shared prose here.\n", true
			}
			return "", false
		},
	})
	if !strings.Contains(out, "Shared prose here.") {
		t.Errorf("include not expanded:\n%s", out)
	}
}

func TestRSTIncludeHonoursRangeOptions(t *testing.T) {
	// The self-include idiom: reuse one passage of a file, not the file.
	src := ".. include:: /self.rst\n   :start-after: BEGIN\n   :end-before: END\n"
	out := RSTOpts(src, Options{
		Width: 70,
		Include: func(string) (string, bool) {
			return "before text\n.. BEGIN\nthe wanted passage\n.. END\nafter text\n", true
		},
	})
	if !strings.Contains(out, "the wanted passage") {
		t.Errorf("range not extracted:\n%s", out)
	}
	if strings.Contains(out, "before text") || strings.Contains(out, "after text") {
		t.Errorf("range bounds not respected:\n%s", out)
	}
}

func TestRSTIncludeCycleTerminates(t *testing.T) {
	// A file including itself without range options is a cycle. Expanding it
	// naively is exponential, so it must be refused rather than followed.
	done := make(chan string, 1)
	go func() {
		done <- RSTOpts("intro\n\n.. include:: /self.rst\n", Options{
			Width: 70,
			Include: func(string) (string, bool) {
				return "body\n\n.. include:: /self.rst\n", true
			},
		})
	}()
	select {
	case out := <-done:
		if !strings.Contains(out, "intro") || !strings.Contains(out, "body") {
			t.Errorf("content lost:\n%s", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("include cycle did not terminate")
	}
}

func TestRSTMissingIncludeIsReported(t *testing.T) {
	out := RSTOpts(".. include:: /gone.txt\n", Options{
		Width:   70,
		Include: func(string) (string, bool) { return "", false },
	})
	if !strings.Contains(out, "could not resolve include /gone.txt") {
		t.Errorf("missing include should be visible:\n%s", out)
	}
}

func TestRSTTabsAreContentNotDecoration(t *testing.T) {
	src := `.. tabs::

   .. group-tab:: Linux

      Run the Linux command.

   .. group-tab:: Windows

      Run the Windows command.
`
	out := RST(src, 70)
	for _, want := range []string{"Linux:", "Run the Linux command.", "Windows:", "Run the Windows command."} {
		if !strings.Contains(out, want) {
			t.Errorf("tab content lost (%q):\n%s", want, out)
		}
	}
}

func TestRSTNestedDirectiveInDefinitionBody(t *testing.T) {
	// A definition body holding a directive must be parsed, not flattened:
	// flattening leaves raw markup in the prose.
	src := `Some term

   .. note::
      Mind this.
`
	out := RST(src, 70)
	if strings.Contains(out, ".. note::") {
		t.Errorf("nested directive was flattened:\n%s", out)
	}
	if !strings.Contains(out, "NOTE:") || !strings.Contains(out, "Mind this.") {
		t.Errorf("nested admonition lost:\n%s", out)
	}
}

func TestRSTGridTableKeepsAlignment(t *testing.T) {
	src := "+------+--------+\n" +
		"| Opt  | Detail |\n" +
		"+======+========+\n" +
		"| ``A``| text   |\n" +
		"+------+--------+\n"
	out := RST(src, 70)
	var rows []string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "+---") || strings.Contains(l, "|") {
			rows = append(rows, l)
		}
	}
	if len(rows) < 4 {
		t.Fatalf("table not preserved:\n%s", out)
	}
	width := len(rows[0])
	for _, r := range rows {
		if len(r) != width {
			t.Errorf("column alignment lost (%d vs %d): %q\nfull:\n%s", len(r), width, r, out)
		}
	}
	if strings.Contains(out, "``") {
		t.Errorf("literal markup should be stripped from cells:\n%s", out)
	}
}

func TestParseToctree(t *testing.T) {
	src := `.. toctree::
   :maxdepth: 2
   :hidden:

   gsg_guides
   installation
   Custom Title <libraries/index>

Prose after the tree.
`
	got, glob := ParseToctree(src)
	want := []string{"gsg_guides", "installation", "libraries/index"}
	if glob {
		t.Error("glob should be false")
	}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestParseSubstitutionsAndLinks(t *testing.T) {
	subs := ParseSubstitutions(".. |NCS| replace:: nRF Connect SDK\n.. |release| replace:: v3.4.0\n")
	if subs["NCS"] != "nRF Connect SDK" || subs["release"] != "v3.4.0" {
		t.Errorf("substitutions: %v", subs)
	}
	links := ParseLinkTargets(".. _`Zephyr`: https://zephyrproject.org/\n.. _internal_label:\n")
	if links["Zephyr"] != "https://zephyrproject.org/" {
		t.Errorf("link targets: %v", links)
	}
	if len(links) != 1 {
		t.Errorf("a bare label is not a hyperlink target: %v", links)
	}
}

func TestParseLabelsAndRefs(t *testing.T) {
	labels := ParseLabels(".. _ug_app_dev:\n.. _device_guides:\n\nTitle\n#####\n")
	if len(labels) != 2 || labels[0] != "ug_app_dev" {
		t.Errorf("labels: %v", labels)
	}
	refs := ParseRefs("See :ref:`create_application` and :ref:`text <configure_application>`.\n")
	if len(refs) != 2 {
		t.Fatalf("refs: %v", refs)
	}
	found := map[string]bool{}
	for _, r := range refs {
		found[r] = true
	}
	if !found["create_application"] || !found["configure_application"] {
		t.Errorf("refs: %v", refs)
	}
}

func TestTitle(t *testing.T) {
	if got := Title(".. _label:\n\nApplication development\n#######################\n\nProse.\n"); got != "Application development" {
		t.Errorf("got %q", got)
	}
	if got := Title("Underlined Only\n===============\n"); got != "Underlined Only" {
		t.Errorf("got %q", got)
	}
}
