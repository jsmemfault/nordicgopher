package text

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// RST converts a reStructuredText document to fixed-width plain text.
//
// This is the converter that matters most for the mirror: the nRF Connect SDK
// and Zephyr documentation are RST, and parsing the source gives real section
// structure, admonitions and literal blocks instead of guessing them back out
// of rendered HTML. It is not a full docutils implementation — it handles the
// constructs that appear in Nordic and Zephyr docs and degrades gracefully on
// the rest, dropping directives that carry no plain-text meaning.
func RST(src string, width int) string {
	return RSTOpts(src, Options{Width: width})
}

// RSTOpts converts reStructuredText with explicit options.
func RSTOpts(src string, o Options) string {
	c := &rstConv{o: o, width: o.width(), seen: map[string]int{}}
	return c.run(src)
}

// ParseSubstitutions reads "|name| replace:: text" definitions, as collected
// in a Sphinx project's shared shortcuts file.
func ParseSubstitutions(src string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(src, "\n") {
		if m := reRSTSubstDef.FindStringSubmatch(line); m != nil {
			out[strings.TrimSpace(m[1])] = strings.TrimSpace(m[2])
		}
	}
	return out
}

// ParseLinkTargets reads named hyperlink targets (".. _`Zephyr`: https://...")
// as collected in a Sphinx project's shared links file. Only targets with an
// absolute URL are returned; a bare label is an internal cross-reference and
// belongs in RefTargets instead.
func ParseLinkTargets(src string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(src, "\n") {
		m := reRSTLinkDef.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		target := strings.TrimSpace(m[2])
		if !strings.Contains(target, "://") && !strings.HasPrefix(target, "mailto:") {
			continue
		}
		out[strings.TrimSpace(m[1])] = target
	}
	return out
}

// ParseLabels reads internal cross-reference labels (".. _ug_app_dev:") so an
// ingester can map them to the documents that define them.
func ParseLabels(src string) []string {
	var out []string
	for _, line := range strings.Split(src, "\n") {
		if m := reRSTLabelDef.FindStringSubmatch(line); m != nil {
			out = append(out, m[1])
		}
	}
	return out
}

// ParseToctree returns the document names listed in every toctree directive
// in src, in order, along with whether any directive used the glob option.
func ParseToctree(src string) (entries []string, glob bool) {
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		m := reRSTDirective.FindStringSubmatch(lines[i])
		if m == nil || strings.ToLower(m[2]) != "toctree" {
			continue
		}
		base := len(m[1])
		for i++; i < len(lines); i++ {
			line := lines[i]
			if strings.TrimSpace(line) == "" {
				// A blank line inside a directive body is allowed; the body
				// ends at the first dedented non-blank line.
				if j := nextNonBlank(lines, i); j >= len(lines) || indentOf(lines[j]) <= base {
					break
				}
				continue
			}
			if indentOf(line) <= base {
				i--
				break
			}
			t := strings.TrimSpace(line)
			if strings.HasPrefix(t, ":") {
				if strings.HasPrefix(t, ":glob:") {
					glob = true
				}
				continue
			}
			// An entry may be written "Title <docname>".
			if k := strings.LastIndex(t, "<"); k >= 0 && strings.HasSuffix(t, ">") {
				t = strings.TrimSpace(t[k+1 : len(t)-1])
			}
			if t != "" {
				entries = append(entries, t)
			}
		}
	}
	return entries, glob
}

// ParseRefs returns the Sphinx cross-reference labels a document links to,
// deduplicated and in order of first use. An ingester uses these to offer the
// referenced pages as selectable menu items, which is the only way to make a
// cross-reference followable in gopherspace: a type-0 text file cannot carry
// a link.
func ParseRefs(src string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(label string) {
		label = strings.TrimSpace(label)
		if label == "" || strings.Contains(label, "://") || seen[label] {
			return
		}
		seen[label] = true
		out = append(out, label)
	}
	for _, m := range reRSTRoleTarget.FindAllStringSubmatch(src, -1) {
		if m[1] == ":ref:" {
			add(m[3])
		}
	}
	for _, m := range reRSTRole.FindAllStringSubmatch(src, -1) {
		if m[1] == ":ref:" && !strings.Contains(m[2], "<") {
			add(m[2])
		}
	}
	return out
}

// ExpandSubstitutions replaces |name| references using subs, leaving unknown
// names as written. Used for titles, which are read outside a conversion.
func ExpandSubstitutions(s string, subs map[string]string) string {
	if len(subs) == 0 {
		return s
	}
	return reRSTSubstRef.ReplaceAllStringFunc(s, func(m string) string {
		if v, ok := subs[reRSTSubstRef.FindStringSubmatch(m)[1]]; ok {
			return v
		}
		return m
	})
}

// Title returns the first section title in an reStructuredText document.
func Title(src string) string {
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], " ")
		if isAdorn(line) && i+2 < len(lines) && strings.TrimSpace(lines[i+1]) != "" && isAdorn(lines[i+2]) {
			return strings.TrimSpace(lines[i+1])
		}
		if i+1 < len(lines) && strings.TrimSpace(line) != "" && isAdorn(lines[i+1]) &&
			len(strings.TrimSpace(lines[i+1])) >= 3 && !reRSTBullet.MatchString(line) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

type rstConv struct {
	o     Options
	width int
	out   []string
	para  []string

	adorn []byte // section adornment characters, in order of first use
	links []string
	seen  map[string]int

	includeDepth int
	includeStack []string // include paths currently being expanded
	depth        int      // nested-block depth
}

// maxNestDepth bounds recursion into nested blocks.
const maxNestDepth = 12

var (
	reRSTDirective = regexp.MustCompile(`^(\s*)\.\.\s+([A-Za-z0-9_-]+)::\s*(.*)$`)
	reRSTComment   = regexp.MustCompile(`^\s*\.\.(\s|$)`)
	reRSTBullet    = regexp.MustCompile(`^(\s*)([*+-]|#\.|\d+[.)])\s+(.*)$`)
	reRSTField     = regexp.MustCompile(`^(\s*):([A-Za-z0-9_ -]+):\s*(.*)$`)
	// An embedded-target reference: `text <target>`_ where the target is
	// either a URL or the name of another target ("`Add-ons <NCS Add-ons_>`_").
	reRSTNamedLink = regexp.MustCompile("`([^`<]+?)\\s*<([^>]+)>`_+")
	// A role with an embedded target: :ref:`text <label>`. The label is a
	// cross-reference, not display text, so it must not be printed.
	reRSTRoleTarget = regexp.MustCompile("(:[a-z0-9:+_-]+:)`~?([^`<]*?)\\s*<([^>`]+)>`")
	// A role or interpreted text without a target: :ref:`label`, ``x``, `y`.
	reRSTRole = regexp.MustCompile("(:[a-z0-9:+_-]+:)?`~?([^`]+)`(_*)")
	// A bare named reference: `Zephyr`_ or Zephyr_.
	reRSTBareRef = regexp.MustCompile(`\b([A-Za-z][A-Za-z0-9._+-]*)_\b`)
	// A substitution reference: |NCS|.
	reRSTSubstRef = regexp.MustCompile(`\|([A-Za-z0-9_ .+-]+)\|`)
	// A substitution definition: .. |NCS| replace:: nRF Connect SDK
	reRSTSubstDef = regexp.MustCompile(`^\s*\.\.\s+\|([^|]+)\|\s+replace::\s*(.*)$`)
	// A named hyperlink target: .. _`Zephyr`: https://zephyrproject.org/
	reRSTLinkDef = regexp.MustCompile("^\\s*\\.\\.\\s+_`?([^`:]+)`?:\\s*(\\S+)\\s*$")
	// An internal label: .. _ug_app_dev:
	reRSTLabelDef = regexp.MustCompile(`^\s*\.\.\s+_([A-Za-z0-9_.+-]+):\s*$`)
	// A grid table rule: +-----+-----+
	reGridRule = regexp.MustCompile(`^\s*\+[-=+]{2,}\+\s*$`)
	// A simple table rule: ====  =========
	reSimpleRule = regexp.MustCompile(`^\s*=+(\s+=+)+\s*$`)
	reRSTLiteral = regexp.MustCompile("``([^`]+)``")
	reRSTStrong  = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	reRSTEm      = regexp.MustCompile(`\*([^*\n]+)\*`)
	reRSTTarget  = regexp.MustCompile(`^\s*\.\.\s+_[^:]+:\s*\S*\s*$`)

	// adornChars are the punctuation characters docutils accepts as section
	// underlines. Order here is irrelevant; level is assigned by first use.
	adornChars = "!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~"
)

// dropDirectives produce no plain-text output at all, body included: either
// they render as navigation the mirror builds itself, or they need a
// generated artefact (Doxygen XML, a rendered graph) that is not available
// from source.
var dropDirectives = map[string]bool{
	"contents": true, "toctree": true, "image": true, "figure": true,
	"raw": true, "highlight": true, "sectionauthor": true,
	"index": true, "meta": true, "graphviz": true, "raw-html": true,
	"doxygengroup": true, "doxygenfunction": true, "doxygenstruct": true,
	"doxygenenum": true, "doxygentypedef": true, "doxygendefine": true,
	"doxygenfile": true, "kconfigdiff": true,
}

// transparentDirectives contribute no text of their own but their bodies are
// content. Dropping them would silently delete documentation: an ".. only::"
// block holds prose, and a ".. tabs::" block holds the per-platform
// instructions that are often the entire point of the page.
var transparentDirectives = map[string]bool{
	"tabs": true, "only": true, "container": true, "rst-class": true,
	"list-table": true, "table": true, "grid": true, "highlights": true,
	"admonition": true, "topic": true, "sidebar": true, "compound": true,
}

// labelledDirectives render their argument as a heading above their body,
// because the argument distinguishes the alternatives: which platform a tab
// applies to, what a collapsed section contains.
var labelledDirectives = map[string]bool{
	"tab": true, "group-tab": true, "toggle": true, "dropdown": true,
}

// admonitions render as a labelled block.
var admonitions = map[string]string{
	"note": "NOTE", "warning": "WARNING", "important": "IMPORTANT",
	"caution": "CAUTION", "tip": "TIP", "attention": "ATTENTION",
	"danger": "DANGER", "seealso": "SEE ALSO", "deprecated": "DEPRECATED",
	"versionadded": "ADDED IN", "versionchanged": "CHANGED IN",
}

// literalDirectives render their body verbatim.
var literalDirectives = map[string]bool{
	"code-block": true, "code": true, "literalinclude": true,
	"parsed-literal": true, "math": true,
}

func (c *rstConv) run(src string) string {
	c.parse(src)
	c.flush()
	c.footnotes()
	return strings.Join(trimBlanks(c.out), "\n") + "\n"
}

// parse appends the conversion of src to the output. It is called again for
// each included file, so it must not finalise anything.
func (c *rstConv) parse(src string) {
	src = strings.ReplaceAll(src, "\r\n", "\n")
	src = strings.ReplaceAll(src, "\t", "    ")
	c.parseLines(strings.Split(src, "\n"))
}

// parseLines is the body of parse. Nested blocks are already split into
// lines, so they go straight here rather than being rejoined first.
func (c *rstConv) parseLines(lines []string) {
	for i := 0; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], " ")

		// Section with overline: adornment, title, matching adornment.
		if isAdorn(line) && i+2 < len(lines) &&
			strings.TrimSpace(lines[i+1]) != "" && isAdorn(lines[i+2]) {
			c.section(line[0], c.inline(strings.TrimSpace(lines[i+1])))
			i += 2
			continue
		}
		// Section with underline only.
		if i+1 < len(lines) && strings.TrimSpace(line) != "" && isAdorn(lines[i+1]) &&
			len(strings.TrimSpace(lines[i+1])) >= 3 &&
			!reRSTBullet.MatchString(line) {
			c.section(strings.TrimSpace(lines[i+1])[0], c.inline(strings.TrimSpace(line)))
			i++
			continue
		}

		if m := reRSTDirective.FindStringSubmatch(line); m != nil {
			i = c.directive(lines, i, m)
			continue
		}
		if reRSTTarget.MatchString(line) || reRSTComment.MatchString(line) {
			i = skipIndented(lines, i)
			continue
		}

		switch {
		case strings.TrimSpace(line) == "":
			c.flush()

		// A paragraph ending in "::" introduces a literal block.
		case strings.HasSuffix(line, "::"):
			c.para = append(c.para, strings.TrimSpace(strings.TrimSuffix(line, "::"))+":")
			c.flush()
			i = c.literalBlock(lines, i+1, "    ")

		case reRSTBullet.MatchString(line):
			m := reRSTBullet.FindStringSubmatch(line)
			c.flush()
			marker := "* "
			if !strings.ContainsAny(m[2], "*+-") {
				marker = m[2] + " "
			}
			indent := strings.Repeat(" ", 2+len(m[1]))
			c.emitAll(Wrap(indent+marker+c.inline(m[3]), c.width, strings.Repeat(" ", len(marker))))

		case reRSTField.MatchString(line):
			m := reRSTField.FindStringSubmatch(line)
			c.flush()
			c.emitAll(Wrap(fmt.Sprintf("  %s: %s", m[2], c.inline(m[3])), c.width, "    "))

		// A table is ASCII art: its shape carries the row and column
		// structure, so the block is emitted verbatim. Reflowing it would
		// destroy the alignment that makes it readable, which costs more
		// than leaving the occasional token unexpanded inside a cell.
		case isTableStart(lines, i):
			c.flush()
			i = c.table(lines, i)

		// A definition list: a term on its own line, its definition indented
		// beneath. These carry most of the structure on nRF Connect SDK
		// overview pages, so folding them into the surrounding paragraph
		// would lose the shape of the document.
		case isDefTerm(lines, i):
			c.flush()
			term := strings.TrimSpace(line)
			body, end := extractBlock(lines, i+1, indentOf(line))
			c.emit("")
			c.emitAll(Wrap("  "+c.inline(term), c.width, "  "))
			c.nested(body, "      ")
			c.emit("")
			i = end - 1

		default:
			c.para = append(c.para, strings.TrimSpace(line))
		}
	}
}

// isTableStart reports whether line i begins a grid or simple table.
func isTableStart(lines []string, i int) bool {
	return reGridRule.MatchString(lines[i]) ||
		(reSimpleRule.MatchString(lines[i]) && i+1 < len(lines) && strings.TrimSpace(lines[i+1]) != "")
}

// table emits a table block verbatim and returns the index of its last line.
func (c *rstConv) table(lines []string, i int) int {
	base := indentOf(lines[i])
	c.emit("")
	for ; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], " ")
		if strings.TrimSpace(line) == "" {
			// A blank line ends a grid table; in a simple table it separates
			// the header, so the block continues only if a rule follows.
			if j := nextNonBlank(lines, i); j < len(lines) &&
				indentOf(lines[j]) >= base && isTablePart(lines[j]) {
				c.emit("")
				continue
			}
			break
		}
		if indentOf(line) < base || !isTablePart(line) {
			break
		}
		c.emit("  " + tidyTableLine(line[base:]))
	}
	c.emit("")
	return i - 1
}

// tidyTableLine removes role and inline-literal markup from a table row
// while preserving column positions.
//
// Alignment is the whole point of a table, so a cell cannot simply be
// rewritten: the "|" separators would drift and the table would stop being
// readable. Both constructs removed here are strictly longer than the text
// they wrap, so the freed columns are replaced with spaces and every
// separator stays exactly where it was. Substitutions are left alone for the
// opposite reason -- their expansion is longer than the reference, so there
// is no room to put it.
func tidyTableLine(line string) string {
	if !strings.Contains(line, "`") {
		return line
	}
	pad := func(re *regexp.Regexp, group int) {
		line = re.ReplaceAllStringFunc(line, func(m string) string {
			inner := re.FindStringSubmatch(m)[group]
			if n := len(m) - len(inner); n >= 0 {
				return inner + strings.Repeat(" ", n)
			}
			return m
		})
	}
	pad(reRSTLiteral, 1)
	pad(reRSTRoleTarget, 2)
	pad(reRSTRole, 2)
	return line
}

// isTablePart reports whether a line belongs to a table block.
func isTablePart(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" {
		return false
	}
	return strings.HasPrefix(t, "+") || strings.HasPrefix(t, "|") ||
		reSimpleRule.MatchString(line) || indentOf(line) > 0
}

// isDefTerm reports whether line i is a definition-list term: a single
// non-blank line immediately followed by a more deeply indented block.
func isDefTerm(lines []string, i int) bool {
	line := lines[i]
	if strings.TrimSpace(line) == "" || i+1 >= len(lines) {
		return false
	}
	next := lines[i+1]
	if strings.TrimSpace(next) == "" {
		return false
	}
	if indentOf(next) <= indentOf(line) {
		return false
	}
	// A term is short and unpunctuated; a wrapped sentence is neither.
	t := strings.TrimSpace(line)
	if len(t) > 60 || strings.HasSuffix(t, ".") || strings.HasSuffix(t, ",") {
		return false
	}
	return !reRSTBullet.MatchString(line) && !reRSTField.MatchString(line)
}

// directive handles one directive and returns the index of its last line.
func (c *rstConv) directive(lines []string, i int, m []string) int {
	indent, name, arg := m[1], strings.ToLower(m[2]), strings.TrimSpace(m[3])
	c.flush()
	base := len(indent)

	switch {
	case name == "include" || name == "ncs-include":
		// Sphinx projects factor shared prose into included files, and the
		// nRF Connect SDK ones carry real content (build steps, board
		// tables), so the include is expanded rather than dropped.
		opts, _ := parseOptions(lines, i+1)
		end := skipIndented(lines, i)
		if c.o.Include == nil || c.includeDepth >= c.o.maxIncludeDepth() {
			return end
		}
		// A document including itself is the standard way to reuse one of
		// its own passages, paired with :start-after: and :end-before:. The
		// stack check is what stops that from recursing without bound, and
		// what stops a genuine cycle between two files.
		if slices.Contains(c.includeStack, arg) && opts["start-after"] == "" && opts["start-line"] == "" {
			return end
		}
		body, ok := c.o.Include(arg)
		if !ok {
			c.logMissing(arg)
			return end
		}
		body = sliceInclude(body, opts)
		if strings.TrimSpace(body) == "" {
			return end
		}

		c.includeDepth++
		c.includeStack = append(c.includeStack, arg)
		if opts["literal"] != "" || opts["code"] != "" {
			c.emit("")
			for _, l := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
				c.emit("    " + l)
			}
			c.emit("")
		} else {
			c.parse(body)
		}
		c.includeStack = c.includeStack[:len(c.includeStack)-1]
		c.includeDepth--
		return end

	case dropDirectives[name]:
		return skipIndented(lines, i)

	case literalDirectives[name]:
		j := skipOptions(lines, i+1)
		c.emit("")
		if arg != "" && name != "literalinclude" {
			c.emit("    (" + arg + ")")
		}
		return c.literalBlock(lines, j, "    ")

	case admonitions[name] != "":
		label := admonitions[name]
		body, end := extractBlock(lines, skipOptions(lines, i+1), base)
		if arg != "" {
			body = append([]string{arg, ""}, body...)
		}
		c.emit("")
		c.emit("  " + label + ":")
		c.nested(body, "  | ")
		c.emit("")
		return end - 1

	case transparentDirectives[name]:
		body, end := extractBlock(lines, skipOptions(lines, i+1), base)
		if arg != "" {
			c.emit("")
			c.emitAll(Wrap("  "+c.inline(arg), c.width, "  "))
		}
		c.nested(body, "")
		return end - 1

	case labelledDirectives[name]:
		body, end := extractBlock(lines, skipOptions(lines, i+1), base)
		c.emit("")
		if arg != "" {
			c.emit("  " + c.inline(arg) + ":")
		}
		c.nested(body, "  ")
		c.emit("")
		return end - 1

	default:
		// An unknown directive: keep the body, drop the markup. Parsing it
		// rather than flattening it means nested structure survives.
		body, end := extractBlock(lines, skipOptions(lines, i+1), base)
		if arg != "" {
			body = append([]string{arg, ""}, body...)
		}
		c.nested(body, "")
		return end - 1
	}
}

// nested converts an already-dedented block and indents the result.
//
// It reuses this converter rather than creating another, so footnote
// numbering and the learned section hierarchy stay shared with the enclosing
// document: the block's output lines are simply indented after the fact.
func (c *rstConv) nested(body []string, indent string) {
	if len(body) == 0 {
		return
	}
	c.flush()
	start := len(c.out)

	if c.depth >= maxNestDepth {
		// Structure this deep is either malformed or not worth
		// distinguishing in plain text; emit it flat rather than recurse.
		for _, l := range body {
			c.emit(indent + l)
		}
		return
	}

	saved := c.width
	if c.width-len(indent) > 20 {
		c.width -= len(indent)
	}
	c.depth++
	c.parseLines(body)
	c.flush()
	c.depth--
	c.width = saved

	for i := start; i < len(c.out); i++ {
		if strings.TrimSpace(c.out[i]) != "" {
			c.out[i] = indent + c.out[i]
		}
	}
}

// extractBlock collects an indented block, dedented by its own indentation so
// that it can be parsed as a document in its own right, and returns the index
// just past it. base is the indentation of the construct introducing it.
func extractBlock(lines []string, i int, base int) ([]string, int) {
	j := nextNonBlank(lines, i)
	if j >= len(lines) || indentOf(lines[j]) <= base {
		return nil, j
	}
	blockIndent := indentOf(lines[j])

	var body []string
	for i = j; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "" {
			if k := nextNonBlank(lines, i); k >= len(lines) || indentOf(lines[k]) <= base {
				break
			}
			body = append(body, "")
			continue
		}
		if indentOf(lines[i]) <= base {
			break
		}
		l := strings.TrimRight(lines[i], " ")
		if len(l) >= blockIndent {
			l = l[blockIndent:]
		} else {
			l = strings.TrimLeft(l, " ")
		}
		body = append(body, l)
	}
	return body, i
}

// literalBlock emits an indented block verbatim and returns its last index.
func (c *rstConv) literalBlock(lines []string, i int, prefix string) int {
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	if i >= len(lines) {
		return i
	}
	base := indentOf(lines[i])
	if base == 0 {
		return i - 1
	}
	c.emit("")
	for ; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "" {
			// A blank line only ends the block if what follows is dedented.
			if j := nextNonBlank(lines, i); j < len(lines) && indentOf(lines[j]) < base {
				break
			}
			c.emit("")
			continue
		}
		if indentOf(lines[i]) < base {
			break
		}
		c.emit(prefix + strings.TrimRight(lines[i][base:], " "))
	}
	c.emit("")
	return i - 1
}

// section assigns a heading level from the adornment character, learning the
// document's own hierarchy as docutils does.
func (c *rstConv) section(ch byte, title string) {
	level := 0
	for i, a := range c.adorn {
		if a == ch {
			level = i + 1
			break
		}
	}
	if level == 0 {
		c.adorn = append(c.adorn, ch)
		level = len(c.adorn)
	}
	c.flush()
	c.emit("")
	switch level {
	case 1:
		c.emit(strings.ToUpper(title))
		c.emit(Rule("=", min(len(title), c.width)))
	case 2:
		c.emit(title)
		c.emit(Rule("-", min(len(title), c.width)))
	default:
		c.emit(strings.Repeat(" ", (level-3)*2) + title + ":")
	}
	c.emit("")
}

func (c *rstConv) flush() {
	if len(c.para) == 0 {
		return
	}
	joined := strings.Join(c.para, " ")
	c.para = nil
	if t := strings.TrimSpace(joined); t != "" {
		c.emitAll(Wrap(c.inline(t), c.width, ""))
		c.emit("")
	}
}

func (c *rstConv) emit(s string) { c.out = append(c.out, s) }

func (c *rstConv) emitAll(ss []string) {
	for _, s := range ss {
		c.emit(s)
	}
}

func (c *rstConv) footnote(target string) string {
	return c.footnoteRaw(c.o.resolve(target))
}

// footnoteRaw records a target that is already final -- a resolved URL, or a
// mirror selector that must not be rewritten against the upstream base URL.
func (c *rstConv) footnoteRaw(target string) string {
	if n, ok := c.seen[target]; ok {
		return fmt.Sprintf(" [%d]", n)
	}
	n := len(c.links) + 1
	c.seen[target] = n
	c.links = append(c.links, target)
	return fmt.Sprintf(" [%d]", n)
}

func (c *rstConv) footnotes() {
	if len(c.links) == 0 {
		return
	}
	c.emit("")
	c.emit("Links")
	c.emit(Rule("-", 5))
	for i, l := range c.links {
		c.emit(fmt.Sprintf("[%d] %s", i+1, l))
	}
}

func (c *rstConv) inline(s string) string {
	// Substitutions come first: they expand into ordinary markup, including
	// roles and references, which the passes below then handle.
	s = c.substitute(s)

	// Inline literals are opaque -- their contents must not be reinterpreted
	// as markup -- so they are set aside and restored at the end.
	var literals []string
	s = reRSTLiteral.ReplaceAllStringFunc(s, func(m string) string {
		literals = append(literals, reRSTLiteral.FindStringSubmatch(m)[1])
		return "\x00" + strconv.Itoa(len(literals)-1) + "\x00"
	})

	s = reRSTNamedLink.ReplaceAllStringFunc(s, func(m string) string {
		g := reRSTNamedLink.FindStringSubmatch(m)
		text, target := g[1], strings.TrimSpace(g[2])
		// An embedded target may itself name another target, written with a
		// trailing underscore: `Add-ons <NCS Add-ons_>`_.
		if strings.HasSuffix(target, "_") {
			if u, ok := c.o.LinkTargets[strings.TrimSuffix(target, "_")]; ok {
				return text + c.footnoteRaw(u)
			}
			return text
		}
		return text + c.footnote(target)
	})

	// A role with an embedded target: the label is a cross-reference, so
	// only the display text survives. When the label is known to the mirror
	// the footnote points at it.
	s = reRSTRoleTarget.ReplaceAllStringFunc(s, func(m string) string {
		g := reRSTRoleTarget.FindStringSubmatch(m)
		text, label := strings.TrimSpace(g[2]), strings.TrimSpace(g[3])
		if text == "" {
			text = label
		}
		if strings.Contains(label, "://") {
			return text + c.footnote(label)
		}
		if sel, ok := c.o.RefTargets[label]; ok {
			return text + c.footnoteRaw(sel)
		}
		return text
	})

	// A role or reference without an embedded target.
	s = reRSTRole.ReplaceAllStringFunc(s, func(m string) string {
		g := reRSTRole.FindStringSubmatch(m)
		role, body, trailing := g[1], strings.TrimSpace(g[2]), g[3]
		// A trailing underscore and no role makes this a named reference.
		if role == "" && trailing != "" {
			if u, ok := c.o.LinkTargets[body]; ok {
				return body + c.footnoteRaw(u)
			}
			return body
		}
		if role == ":ref:" {
			if sel, ok := c.o.RefTargets[body]; ok {
				return body + c.footnoteRaw(sel)
			}
		}
		return body
	})

	// A bare named reference: Zephyr_ rather than `Zephyr`_.
	s = reRSTBareRef.ReplaceAllStringFunc(s, func(m string) string {
		name := reRSTBareRef.FindStringSubmatch(m)[1]
		if u, ok := c.o.LinkTargets[name]; ok {
			return name + c.footnoteRaw(u)
		}
		return m
	})

	s = reRSTStrong.ReplaceAllString(s, "$1")
	s = reRSTEm.ReplaceAllString(s, "$1")

	for i, l := range literals {
		s = strings.ReplaceAll(s, "\x00"+strconv.Itoa(i)+"\x00", l)
	}
	return strings.TrimSpace(s)
}

// substitute expands |name| references. An unknown substitution is left as
// written rather than deleted: a visible |name| in the output signals a
// missing definition, whereas silently dropping it corrupts the sentence.
func (c *rstConv) substitute(s string) string {
	if len(c.o.Substitutions) == 0 {
		return s
	}
	for range 3 { // substitutions may nest a level or two
		before := s
		s = reRSTSubstRef.ReplaceAllStringFunc(s, func(m string) string {
			name := reRSTSubstRef.FindStringSubmatch(m)[1]
			if v, ok := c.o.Substitutions[name]; ok {
				return v
			}
			return m
		})
		if s == before {
			break
		}
	}
	return s
}

// logMissing records an unresolvable include as a visible marker. Silently
// dropping it would leave a gap in the document with no indication why.
func (c *rstConv) logMissing(path string) {
	c.emit("")
	c.emit("  [omitted: could not resolve include " + path + "]")
	c.emit("")
}

func isAdorn(line string) bool {
	t := strings.TrimSpace(line)
	if len(t) < 3 {
		return false
	}
	if !strings.ContainsRune(adornChars, rune(t[0])) {
		return false
	}
	return strings.Trim(t, string(t[0])) == ""
}

func indentOf(s string) int { return len(s) - len(strings.TrimLeft(s, " ")) }

func nextNonBlank(lines []string, i int) int {
	for ; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != "" {
			return i
		}
	}
	return i
}

// parseOptions reads a directive's ":option: value" lines, returning them and
// the index of the first line past them. An option present without a value is
// recorded as "true" so its presence can be tested.
func parseOptions(lines []string, i int) (map[string]string, int) {
	opts := map[string]string{}
	for i < len(lines) && reRSTField.MatchString(lines[i]) && indentOf(lines[i]) > 0 {
		m := reRSTField.FindStringSubmatch(lines[i])
		v := strings.TrimSpace(m[3])
		if v == "" {
			v = "true"
		}
		opts[strings.ToLower(strings.TrimSpace(m[2]))] = v
		i++
	}
	return opts, i
}

// sliceInclude applies the include directive's line- and text-range options.
// Honouring them is what makes a self-include mean "reuse this passage"
// rather than "expand the whole document again".
func sliceInclude(body string, opts map[string]string) string {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")

	if v := opts["start-after"]; v != "" && v != "true" {
		for i, l := range lines {
			if strings.Contains(l, v) {
				lines = lines[i+1:]
				break
			}
		}
	}
	if v := opts["end-before"]; v != "" && v != "true" {
		for i, l := range lines {
			if strings.Contains(l, v) {
				lines = lines[:i]
				break
			}
		}
	}
	// Line numbers are 1-based and inclusive of start, exclusive of end,
	// matching docutils.
	if n, err := strconv.Atoi(opts["start-line"]); err == nil && n > 0 && n <= len(lines) {
		lines = lines[n-1:]
	}
	if n, err := strconv.Atoi(opts["end-line"]); err == nil && n > 0 && n <= len(lines) {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

// skipOptions consumes a directive's ":option:" lines.
func skipOptions(lines []string, i int) int {
	for i < len(lines) && reRSTField.MatchString(lines[i]) && indentOf(lines[i]) > 0 {
		i++
	}
	return i
}

// skipIndented discards a block and everything indented under it.
func skipIndented(lines []string, i int) int {
	base := indentOf(lines[i])
	for i++; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		if indentOf(lines[i]) <= base {
			return i - 1
		}
	}
	return i - 1
}
