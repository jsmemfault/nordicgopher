package gopher

import (
	"bufio"
	"io"
	"strconv"
	"strings"
)

// ParseGophermap reads the on-disk menu format used by Bucktooth and
// Gophernicus, so that a generated content tree can also be served by those
// implementations without changes.
//
// A line containing a tab is an item: the first character is the item type,
// the rest of field one is the display string, and the remaining
// tab-separated fields are selector, host and port. Host and port may be
// omitted to mean "this server". Any line without a tab is an informational
// line reproduced verbatim, which is what makes prose and ASCII layout work.
// Lines beginning with "#" are comments.
func ParseGophermap(r io.Reader) (Menu, error) {
	var m Menu
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.Contains(line, "\t") {
			m = append(m, Info(line))
			continue
		}
		fields := strings.Split(line, "\t")
		it := Item{Type: Type(fields[0][0]), Display: fields[0][1:]}
		if len(fields) > 1 {
			it.Selector = fields[1]
		}
		if len(fields) > 2 && fields[2] != "" {
			it.Host = fields[2]
		}
		if len(fields) > 3 {
			if p, err := strconv.Atoi(strings.TrimSpace(fields[3])); err == nil {
				it.Port = p
			}
		}
		// An 'i' line the generator wrote explicitly still needs the dummy
		// host that clients expect on non-selectable items.
		if it.Type == TypeInfo && it.Host == "" {
			it.Host, it.Port = "error.host", 1
		}
		m = append(m, it)
	}
	return m, sc.Err()
}

// FormatGophermap renders a menu back into the on-disk format.
//
// Informational lines are written with an explicit "i" type and a trailing
// tab rather than as bare text. Bucktooth and Gophernicus infer the type from
// the absence of a tab, but Motsognir does not: it reads the first character
// of every non-empty line as the item type, so a bare banner line would be
// served as an item of type ' ' or '='. Being explicit is valid in all three,
// and it is what lets this tree be served by an existing Motsognir instance
// instead of by this server.
func FormatGophermap(w io.Writer, m Menu) error {
	bw := bufio.NewWriter(w)
	for _, it := range m {
		if it.Type == TypeInfo && it.Host == "error.host" {
			bw.WriteByte(byte(TypeInfo))
			bw.WriteString(clean(it.Display))
			bw.WriteString("\t\n")
			continue
		}
		bw.WriteByte(byte(it.Type))
		bw.WriteString(clean(it.Display))
		bw.WriteByte('\t')
		bw.WriteString(clean(it.Selector))
		if it.Host != "" {
			bw.WriteByte('\t')
			bw.WriteString(clean(it.Host))
			bw.WriteByte('\t')
			bw.WriteString(strconv.Itoa(it.Port))
		}
		bw.WriteString("\n")
	}
	return bw.Flush()
}
