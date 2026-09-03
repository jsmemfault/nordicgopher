// Package text reflows source documents into the fixed-width plain text that
// Gopher clients expect.
package text

import "strings"

// Width is the column at which mirrored text is wrapped. Gopher clients
// assume an 80-column terminal; leaving a margin keeps quoted and indented
// text from wrapping again on the client side.
const Width = 70

// Wrap breaks a paragraph at word boundaries. The leading whitespace of the
// first line is preserved, and hang is *additional* indentation applied to
// continuation lines on top of it -- pass "" to align continuations with the
// first line, or the width of a list marker to align them under its text.
func Wrap(s string, width int, hang string) []string {
	indent := s[:len(s)-len(strings.TrimLeft(s, " \t"))]
	words := strings.Fields(s)
	if len(words) == 0 {
		return []string{""}
	}

	var out []string
	line := indent + words[0]
	prefix := indent + hang
	for _, w := range words[1:] {
		// A long unbreakable token (a URL, a symbol name) is allowed to
		// overrun rather than be mangled.
		if len(line)+1+len(w) > width && len(strings.TrimSpace(line)) > 0 {
			out = append(out, line)
			line = prefix + w
			continue
		}
		line += " " + w
	}
	return append(out, line)
}

// Center pads s so it sits in the middle of width columns.
func Center(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return strings.Repeat(" ", (width-len(s))/2) + s
}

// Rule returns a horizontal rule of the given character.
func Rule(c string, width int) string {
	return strings.Repeat(c, width)
}

// Truncate shortens s to width columns, marking the cut with an ellipsis so
// that menu display strings stay inside the conventional line length.
func Truncate(s string, width int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= width {
		return s
	}
	if width <= 3 {
		return s[:width]
	}
	return s[:width-3] + "..."
}
