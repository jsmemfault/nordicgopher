package text

import (
	"fmt"
	"regexp"
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

type rstConv struct {
	o     Options
	width int
	out   []string
	para  []string

	adorn []byte // section adornment characters, in order of first use
	links []string
	seen  map[string]int
}

var (
	reRSTDirective = regexp.MustCompile(`^(\s*)\.\.\s+([A-Za-z0-9_-]+)::\s*(.*)$`)
	reRSTComment   = regexp.MustCompile(`^\s*\.\.(\s|$)`)
	reRSTBullet    = regexp.MustCompile(`^(\s*)([*+-]|#\.|\d+[.)])\s+(.*)$`)
	reRSTField     = regexp.MustCompile(`^(\s*):([A-Za-z0-9_ -]+):\s*(.*)$`)
	reRSTNamedLink = regexp.MustCompile("`([^`<]+?)\\s*<([^>]+)>`_+")
	reRSTRole      = regexp.MustCompile("(?::[a-z:]+:)?`~?([^`]+)`_*")
	reRSTLiteral   = regexp.MustCompile("``([^`]+)``")
	reRSTStrong    = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	reRSTEm        = regexp.MustCompile(`\*([^*\n]+)\*`)
	reRSTSubst     = regexp.MustCompile(`\|[A-Za-z0-9_ -]+\|`)
	reRSTTarget    = regexp.MustCompile(`^\s*\.\.\s+_[^:]+:\s*\S*\s*$`)

	// adornChars are the punctuation characters docutils accepts as section
	// underlines. Order here is irrelevant; level is assigned by first use.
	adornChars = "!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~"
)

// dropDirectives produce no plain-text output at all, body included.
var dropDirectives = map[string]bool{
	"contents": true, "toctree": true, "image": true, "figure": true,
	"raw": true, "only": true, "highlight": true, "sectionauthor": true,
	"index": true, "meta": true, "tabs": true, "graphviz": true,
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
	src = strings.ReplaceAll(src, "\r\n", "\n")
	src = strings.ReplaceAll(src, "\t", "    ")
	lines := strings.Split(src, "\n")

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

		default:
			c.para = append(c.para, strings.TrimSpace(line))
		}
	}
	c.flush()
	c.footnotes()
	return strings.Join(trimBlanks(c.out), "\n") + "\n"
}

// directive handles one directive and returns the index of its last line.
func (c *rstConv) directive(lines []string, i int, m []string) int {
	indent, name, arg := m[1], strings.ToLower(m[2]), strings.TrimSpace(m[3])
	c.flush()

	switch {
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
		body, j := gatherIndented(lines, i+1, indent)
		if arg != "" {
			body = append([]string{arg}, body...)
		}
		c.emit("")
		text := strings.Join(body, " ")
		if strings.TrimSpace(text) == "" {
			c.emit("  " + label)
		} else {
			c.emitAll(Wrap("  "+label+": "+c.inline(text), c.width, "  "))
		}
		c.emit("")
		return j - 1

	default:
		// Unknown directive: keep the body as prose, drop the markup.
		j := skipOptions(lines, i+1)
		body, end := gatherIndented(lines, j, indent)
		if arg != "" {
			body = append([]string{arg}, body...)
		}
		if t := strings.TrimSpace(strings.Join(body, " ")); t != "" {
			c.emitAll(Wrap(c.inline(t), c.width, ""))
			c.emit("")
		}
		return end - 1
	}
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
	target = c.o.resolve(target)
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
	s = reRSTLiteral.ReplaceAllString(s, "$1")
	s = reRSTNamedLink.ReplaceAllStringFunc(s, func(m string) string {
		g := reRSTNamedLink.FindStringSubmatch(m)
		return g[1] + c.footnote(g[2])
	})
	// Roles (:ref:`x`, :c:func:`y`) and plain interpreted text keep their
	// content; the cross-reference target is meaningless off-site.
	s = reRSTRole.ReplaceAllString(s, "$1")
	s = reRSTStrong.ReplaceAllString(s, "$1")
	s = reRSTEm.ReplaceAllString(s, "$1")
	s = reRSTSubst.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
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

// skipOptions consumes a directive's ":option:" lines.
func skipOptions(lines []string, i int) int {
	for i < len(lines) && reRSTField.MatchString(lines[i]) && indentOf(lines[i]) > 0 {
		i++
	}
	return i
}

// gatherIndented collects the body of a block indented past base.
func gatherIndented(lines []string, i int, baseIndent string) ([]string, int) {
	base := len(baseIndent)
	var body []string
	for ; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "" {
			if j := nextNonBlank(lines, i); j < len(lines) && indentOf(lines[j]) <= base {
				break
			}
			continue
		}
		if indentOf(lines[i]) <= base {
			break
		}
		body = append(body, strings.TrimSpace(lines[i]))
	}
	return body, i
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
