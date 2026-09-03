// Command ngconv converts a single Markdown or reStructuredText file to the
// plain text the mirror serves. It exists to make conversion bugs easy to see
// without running a full ingest.
package main

import (
	"flag"
	"fmt"
	"os"

	"nordicgopher/internal/text"
)

func main() {
	width := flag.Int("width", text.Width, "wrap width")
	base := flag.String("base", "", "base URL for resolving relative links")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: ngconv [-width N] <file.md|file.rst>")
		os.Exit(2)
	}
	b, err := os.ReadFile(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Print(text.AutoOpts(flag.Arg(0), string(b), text.Options{Width: *width, BaseURL: *base}))
}
