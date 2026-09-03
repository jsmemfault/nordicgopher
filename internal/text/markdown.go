package text

import (
	"fmt"
	"regexp"
	"strings"
)

// Markdown converts a Markdown document to fixed-width plain text suitable
// for a Gopher type-0 item.
func Markdown(src string, width int) string {
	return MarkdownOpts(src, Options{Width: width})
}

// MarkdownOpts converts Markdown with explicit options.
//
// Gopher text files have no inline link concept, so links become numbered
// footnote markers collected into a "Links" section at the end. This is
// deliberately a pragmatic converter aimed at repository READMEs, not a
// conforming CommonMark implementation: it handles the constructs that appear
// in practice and discards decoration (badges, alignment HTML, emphasis
// markers) that carries no information in plain text.
func MarkdownOpts(src string, o Options) string {
	c := &mdConv{o: o, width: o.width(), refs: map[string]string{}, seen: map[string]int{}}
	c.parse(src)
	c.footnotes()
	return strings.Join(trimBlanks(c.out), "\n") + "\n"
}

type mdConv struct {
	o     Options
	width int
	out   []string
	para  []string

	refs   map[string]string // reference-style link definitions
	links  []string          // footnote targets, in order of first use
	seen   map[string]int    // target -> footnote number
	inList bool              // last block was a list item

	// paraIndent is the source indentation of the paragraph being gathered.
	// A paragraph that starts flush left ends the enclosing list, even if a
	// list item preceded it; an indented one continues the item.
	paraIndent int
}

var (
	reRefDef  = regexp.MustCompile(`(?m)^\s{0,3}\[([^\]]+)\]:\s*(\S+)`)
	reHeading = regexp.MustCompile(`^\s{0,3}(#{1,6})\s+(.*?)\s*#*\s*$`)
	reBullet  = regexp.MustCompile(`^(\s*)([-*+]|\d+[.)])\s+(.*)$`)
	reFence   = regexp.MustCompile("^\\s{0,3}(```+|~~~+)(.*)$")

	// reBadge matches a link whose entire body is an image: the build-status
	// and download badges that head most READMEs. They must be removed before
	// image and link handling, or the surrounding brackets are left orphaned.
	reBadge    = regexp.MustCompile(`\[\s*!\[[^\]]*\]\([^)]*\)\s*\]\([^)]*\)`)
	reImage    = regexp.MustCompile(`!\[([^\]]*)\]\(([^)\s]*)(?:\s+"[^"]*")?\)`)
	reLink     = regexp.MustCompile(`\[([^\]]*)\]\(([^)\s]*)(?:\s+"[^"]*")?\)`)
	reLinkRef  = regexp.MustCompile(`\[([^\]]+)\]\[([^\]]*)\]`)
	reAutolink = regexp.MustCompile(`<((?:https?|gopher|mailto):[^>\s]+)>`)
	reCodeSpan = regexp.MustCompile("`+([^`]+)`+")
	reStrong   = regexp.MustCompile(`\*\*([^*]+)\*\*|__([^_]+)__`)
	reEm       = regexp.MustCompile(`\*([^*\n]+)\*|(?:^|\s)_([^_\n]+)_`)
	reHTMLTag  = regexp.MustCompile(`</?[a-zA-Z][^>]*>`)
	reAlert    = regexp.MustCompile(`^\[!([A-Za-z]+)\]\s*$`)
	reEntity   = strings.NewReplacer("&nbsp;", " ", "&amp;", "&", "&lt;", "<",
		"&gt;", ">", "&quot;", `"`, "&#39;", "'", "&mdash;", "--", "&ndash;", "-")
)

func (c *mdConv) parse(src string) {
	src = strings.ReplaceAll(src, "\r\n", "\n")
	for _, m := range reRefDef.FindAllStringSubmatch(src, -1) {
		c.refs[strings.ToLower(m[1])] = m[2]
	}

	lines := strings.Split(src, "\n")
	inFence, fenceMark := false, ""

	for i := 0; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], " \t")

		if m := reFence.FindStringSubmatch(line); m != nil {
			if !inFence {
				inFence, fenceMark = true, m[1][:3]
				c.flush()
				c.emit("")
			} else if strings.HasPrefix(strings.TrimSpace(line), fenceMark) {
				inFence = false
				c.emit("")
			}
			continue
		}
		if inFence {
			c.emit("    " + line)
			continue
		}

		switch {
		case strings.TrimSpace(line) == "":
			c.flush()

		case reRefDef.MatchString(line):
			// Definition already harvested; it has no visible text.

		case reHeading.MatchString(line):
			m := reHeading.FindStringSubmatch(line)
			c.heading(len(m[1]), c.inline(m[2]))

		case isHR(line):
			c.flush()
			c.emit("")
			c.emit(Rule("-", c.width))

		case strings.HasPrefix(strings.TrimSpace(line), ">"):
			i = c.blockquote(lines, i)

		case reBullet.MatchString(line):
			m := reBullet.FindStringSubmatch(line)
			c.flush()
			marker := "* "
			if !strings.ContainsAny(m[2], "-*+") {
				marker = m[2] + " "
			}
			indent := strings.Repeat(" ", 2+len(m[1]))
			c.emitAll(Wrap(indent+marker+c.inline(m[3]), c.width, strings.Repeat(" ", len(marker))))
			c.inList = true

		// An indented line is a code block only outside a list. Inside one it
		// is a continuation of the preceding item, which is the far more
		// common case in READMEs and must not be emitted verbatim.
		case indentOf(line) >= 4 || strings.HasPrefix(line, "\t"):
			if c.inList {
				c.addPara(line)
				continue
			}
			c.flush()
			c.emit(strings.Replace(line, "\t", "    ", 1))

		case strings.HasPrefix(strings.TrimSpace(line), "|"):
			c.flush()
			c.emit("  " + strings.TrimSpace(line))
			c.inList = false

		// A setext underline retroactively promotes the pending paragraph.
		case len(c.para) == 1 && isSetext(line):
			h := c.para[0]
			c.para = nil
			level := 1
			if strings.HasPrefix(strings.TrimSpace(line), "-") {
				level = 2
			}
			c.heading(level, c.inline(h))

		default:
			c.addPara(line)
		}
	}
	c.flush()
}

// addPara appends a line to the pending paragraph, remembering the source
// indentation of its first line.
func (c *mdConv) addPara(line string) {
	if len(c.para) == 0 {
		c.paraIndent = indentOf(line)
	}
	c.para = append(c.para, strings.TrimSpace(line))
}

// blockquote gathers a quote block and converts its contents recursively, so
// that lists and paragraphs nested inside a quote reflow correctly instead of
// being wrapped line by line. Returns the index of the block's last line.
func (c *mdConv) blockquote(lines []string, i int) int {
	c.flush()
	var inner []string
	for ; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(t, ">") {
			break
		}
		inner = append(inner, strings.TrimPrefix(strings.TrimPrefix(t, ">"), " "))
	}

	// GitHub alert syntax: the first line is a bare "[!NOTE]" label.
	label := ""
	if len(inner) > 0 {
		if m := reAlert.FindStringSubmatch(strings.TrimSpace(inner[0])); m != nil {
			label = strings.ToUpper(m[1])
			inner = inner[1:]
		}
	}

	sub := &mdConv{o: c.o, width: c.width - 4, refs: c.refs, seen: c.seen}
	sub.links = c.links
	sub.parse(strings.Join(inner, "\n"))
	c.links, c.seen = sub.links, sub.seen

	c.emit("")
	if label != "" {
		c.emit("  | " + label)
	}
	for _, l := range trimBlanks(sub.out) {
		if strings.TrimSpace(l) == "" {
			c.emit("  |")
			continue
		}
		c.emit("  | " + l)
	}
	c.emit("")
	c.inList = false
	return i - 1
}

func (c *mdConv) heading(level int, text string) {
	c.flush()
	c.inList = false
	c.emit("")
	switch level {
	case 1:
		c.emit(strings.ToUpper(text))
		c.emit(Rule("=", min(len(text), c.width)))
	case 2:
		c.emit(text)
		c.emit(Rule("-", min(len(text), c.width)))
	default:
		c.emit(strings.Repeat(" ", (level-3)*2) + text + ":")
	}
	c.emit("")
}

func (c *mdConv) flush() {
	if len(c.para) == 0 {
		return
	}
	joined := strings.Join(c.para, " ")
	c.para = nil
	hang := ""
	if c.inList && c.paraIndent >= 2 {
		hang = "    "
	} else {
		c.inList = false
	}
	c.emitAll(Wrap(hang+c.inline(joined), c.width, hang))
	c.emit("")
}

func (c *mdConv) emit(s string) { c.out = append(c.out, s) }

func (c *mdConv) emitAll(ss []string) {
	for _, s := range ss {
		c.emit(s)
	}
}

// footnote records a link target and returns its marker.
func (c *mdConv) footnote(target string) string {
	target = c.o.resolve(target)
	if n, ok := c.seen[target]; ok {
		return fmt.Sprintf(" [%d]", n)
	}
	n := len(c.links) + 1
	c.seen[target] = n
	c.links = append(c.links, target)
	return fmt.Sprintf(" [%d]", n)
}

func (c *mdConv) footnotes() {
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

// inline rewrites span-level Markdown into plain text, replacing links with
// footnote markers.
func (c *mdConv) inline(s string) string {
	s = reBadge.ReplaceAllString(s, "")
	s = reImage.ReplaceAllStringFunc(s, func(m string) string {
		alt := reImage.FindStringSubmatch(m)[1]
		// Keep alt text only when it is descriptive; badge and icon alts are
		// a word or two and add nothing to a text mirror.
		if len(strings.Fields(alt)) < 3 {
			return ""
		}
		return "[image: " + alt + "]"
	})
	s = reAutolink.ReplaceAllString(s, "$1")
	s = reLink.ReplaceAllStringFunc(s, func(m string) string {
		g := reLink.FindStringSubmatch(m)
		text, target := g[1], g[2]
		if target == "" {
			return text
		}
		if text == "" {
			text = target
		}
		return text + c.footnote(target)
	})
	s = reLinkRef.ReplaceAllStringFunc(s, func(m string) string {
		g := reLinkRef.FindStringSubmatch(m)
		label := g[2]
		if label == "" {
			label = g[1]
		}
		if target, ok := c.refs[strings.ToLower(label)]; ok {
			return g[1] + c.footnote(target)
		}
		return g[1]
	})
	s = reCodeSpan.ReplaceAllString(s, "$1")
	s = reStrong.ReplaceAllString(s, "$1$2")
	s = reEm.ReplaceAllString(s, "$1$2")
	s = reHTMLTag.ReplaceAllString(s, "")
	s = reEntity.Replace(s)
	return strings.TrimSpace(s)
}

// isHR reports a Markdown thematic break. RE2 has no backreferences, so the
// "three or more of the same character" rule is checked directly.
func isHR(line string) bool {
	t := strings.ReplaceAll(strings.TrimSpace(line), " ", "")
	if len(t) < 3 {
		return false
	}
	switch t[0] {
	case '-', '*', '_':
		return strings.Trim(t, string(t[0])) == ""
	}
	return false
}

func isSetext(line string) bool {
	t := strings.TrimSpace(line)
	if len(t) < 2 {
		return false
	}
	return strings.Trim(t, "=") == "" || strings.Trim(t, "-") == ""
}

// trimBlanks collapses runs of blank lines and trims the document ends.
func trimBlanks(lines []string) []string {
	var out []string
	blank := 0
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			blank++
			if blank > 1 || len(out) == 0 {
				continue
			}
			out = append(out, "")
			continue
		}
		blank = 0
		out = append(out, l)
	}
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	return out
}
