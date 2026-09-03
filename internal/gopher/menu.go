package gopher

import (
	"bufio"
	"io"
	"strconv"
)

// MenuWidth is the conventional maximum display-string width for a menu line.
// Gopher clients historically assumed an 80-column terminal and reserved a few
// columns for their own item-type gutter.
const MenuWidth = 70

// Menu is an ordered list of items, written as a Gopher directory listing.
type Menu []Item

// Add appends items and returns the menu, for chaining.
func (m *Menu) Add(items ...Item) *Menu {
	*m = append(*m, items...)
	return m
}

// WriteTo encodes the menu. Items with no Host adopt host and port, so that
// generated content does not have to know where it will be served from.
// The listing is terminated by the "." line required by RFC 1436.
func (m Menu) WriteTo(w io.Writer, host string, port int) error {
	bw := bufio.NewWriter(w)
	for _, it := range m {
		h, p := it.Host, it.Port
		if h == "" {
			h, p = host, port
		}
		if p == 0 {
			p = port
		}
		bw.WriteByte(byte(it.Type))
		bw.WriteString(clean(it.Display))
		bw.WriteByte('\t')
		bw.WriteString(clean(it.Selector))
		bw.WriteByte('\t')
		bw.WriteString(clean(h))
		bw.WriteByte('\t')
		bw.WriteString(strconv.Itoa(p))
		bw.WriteString("\r\n")
	}
	bw.WriteString(".\r\n")
	return bw.Flush()
}

// WriteText sends a file as a Gopher type-0 item: CRLF line endings, lines
// beginning with "." escaped by doubling, and a terminating "." line.
func WriteText(w io.Writer, r io.Reader) error {
	bw := bufio.NewWriter(w)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if len(line) > 0 && line[0] == '.' {
			bw.WriteByte('.')
		}
		bw.WriteString(line)
		bw.WriteString("\r\n")
	}
	if err := sc.Err(); err != nil {
		return err
	}
	bw.WriteString(".\r\n")
	return bw.Flush()
}

// WriteError sends a type-3 error menu, the only in-band way to report a
// failure to a Gopher client.
func WriteError(w io.Writer, msg string) error {
	m := Menu{{Type: TypeError, Display: msg, Selector: "", Host: "error.host", Port: 1}}
	return m.WriteTo(w, "error.host", 1)
}
