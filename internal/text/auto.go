package text

import (
	"path/filepath"
	"strings"
)

// Auto converts a document by filename extension, falling back to reflowing
// the source as plain text so an unknown format still yields something
// readable.
func Auto(filename, src string, width int) string {
	return AutoOpts(filename, src, Options{Width: width})
}

// AutoOpts converts by extension with explicit options.
func AutoOpts(filename, src string, o Options) string {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".md", ".markdown", ".mdown":
		return MarkdownOpts(src, o)
	case ".rst", ".rest":
		return RSTOpts(src, o)
	default:
		return Plain(src, o.width())
	}
}

// Plain reflows already-plain text, leaving indented lines untouched so that
// tables and code samples survive.
func Plain(src string, width int) string {
	src = strings.ReplaceAll(src, "\r\n", "\n")
	var out []string
	for _, line := range strings.Split(src, "\n") {
		line = strings.TrimRight(line, " \t")
		if len(line) <= width || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			out = append(out, line)
			continue
		}
		out = append(out, Wrap(line, width, "")...)
	}
	return strings.Join(trimBlanks(out), "\n") + "\n"
}
