// Command ngconv converts a single Markdown or reStructuredText file to the
// plain text the mirror serves. It exists to make conversion bugs easy to see
// without running a full ingest.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"nordicgopher/internal/text"
)

func main() {
	width := flag.Int("width", text.Width, "wrap width")
	base := flag.String("base", "", "base URL for resolving relative links")
	subs := flag.String("subs", "", "reStructuredText file of |name| replace:: definitions")
	links := flag.String("links", "", "reStructuredText file of named hyperlink targets")
	incDir := flag.String("include-dir", "", "directory to resolve .. include:: paths against")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: ngconv [flags] <file.md|file.rst>")
		flag.PrintDefaults()
		os.Exit(2)
	}

	b, err := os.ReadFile(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	o := text.Options{Width: *width, BaseURL: *base}
	if *subs != "" {
		o.Substitutions = text.ParseSubstitutions(mustRead(*subs))
	}
	if *links != "" {
		o.LinkTargets = text.ParseLinkTargets(mustRead(*links))
	}
	if *incDir != "" {
		dir := *incDir
		o.Include = func(p string) (string, bool) {
			c, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
			if err != nil {
				return "", false
			}
			return string(c), true
		}
	}

	fmt.Print(text.AutoOpts(flag.Arg(0), string(b), o))
}

func mustRead(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	return string(b)
}
