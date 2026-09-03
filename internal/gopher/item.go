// Package gopher implements the parts of RFC 1436 that a read-only content
// server needs: menu encoding, the common item types, and a TCP listener.
package gopher

import "strings"

// Type is a Gopher item type character. The types below are the ones this
// server emits; 'i' and 'h' are not in RFC 1436 but are understood by every
// client in current use.
type Type byte

const (
	TypeText    Type = '0' // plain text file
	TypeMenu    Type = '1' // directory / menu
	TypeError   Type = '3' // error
	TypeArchive Type = '5' // DOS/zip archive
	TypeSearch  Type = '7' // index-search server
	TypeBinary  Type = '9' // binary file
	TypeGIF     Type = 'g'
	TypeImage   Type = 'I'
	TypeHTML    Type = 'h' // selector is "URL:<url>"
	TypeInfo    Type = 'i' // informational, not selectable
)

// Item is one line of a Gopher menu.
type Item struct {
	Type     Type
	Display  string
	Selector string
	Host     string // empty means "this server"
	Port     int    // zero means "this server"
}

// Info returns a non-selectable text line.
func Info(display string) Item {
	return Item{Type: TypeInfo, Display: display, Selector: "fake", Host: "error.host", Port: 1}
}

// Blank returns an empty informational line.
func Blank() Item { return Info("") }

// Link returns an item pointing at a selector on this server.
func Link(t Type, display, selector string) Item {
	return Item{Type: t, Display: display, Selector: selector}
}

// URL returns an 'h' item pointing at an off-gopher URL, used to cite the
// canonical source of mirrored content.
func URL(display, url string) Item {
	return Item{Type: TypeHTML, Display: display, Selector: "URL:" + url, Host: "error.host", Port: 80}
}

// clean strips the characters that would corrupt a menu line. Tabs are the
// field separator and CR/LF terminate the line, so neither can survive inside
// a field.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '\t', '\r', '\n':
			return ' '
		}
		if r < 0x20 {
			return -1
		}
		return r
	}, s)
}
