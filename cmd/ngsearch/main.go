// Command ngsearch answers Gopher type-7 search queries as a CGI script.
//
// It exists so the mirror can be served by an existing gopher daemon --
// Motsognir, in the deployment this was written for -- and keep its search.
// Motsognir executes a selector ending in .cgi, passes the search terms in
// QUERY_STRING_SEARCH, and copies the script's stdout to the client.
//
// A CGI starts fresh per query, so it reads a prebuilt index rather than
// scanning the corpus. Snippets are the exception: they need the document
// text, so the matched files are read afterwards, and only the ones actually
// being shown.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"nordicgopher/internal/gopher"
	"nordicgopher/internal/index"
	"nordicgopher/internal/search"
	"nordicgopher/internal/text"
)

func main() {
	// Defaults suit an installed tree; flags exist so the same binary can be
	// run by hand for testing.
	indexPath := flag.String("index", envOr("NG_INDEX", "/var/lib/nordicgopher/content/search.idx"),
		"index file written by ngingest")
	root := flag.String("root", envOr("NG_ROOT", "/var/lib/nordicgopher/content"),
		"content tree, read only for result snippets")
	prefix := flag.String("prefix", envOr("NG_PREFIX", ""),
		"selector prefix the tree is served under")
	limit := flag.Int("results", 50, "maximum results to show")
	flag.Parse()

	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()

	host, port := serverIdentity()
	query := searchQuery(flag.Args())

	m, err := run(*indexPath, *root, *prefix, query, *limit)
	if err != nil {
		// A CGI's only channel to the user is the menu it prints, so a
		// failure has to be reported as a gopher error item rather than to
		// stderr where nobody will see it.
		fmt.Fprintln(os.Stderr, "ngsearch:", err)
		gopher.WriteError(out, "search is unavailable: "+err.Error())
		return
	}
	m.WriteTo(out, host, port)
}

func run(indexPath, root, prefix, query string, limit int) (gopher.Menu, error) {
	prefix = strings.TrimSuffix(prefix, "/")

	if strings.TrimSpace(query) == "" {
		var m gopher.Menu
		m.Add(gopher.Info("Enter one or more words to search for."))
		m.Add(gopher.Info(""))
		m.Add(gopher.Info("Terms match the beginning of a word, so \"nrf54\" finds"))
		m.Add(gopher.Info("nrf54l15 and nrf54h20. Every term must match."))
		m.Add(gopher.Info(""))
		m.Add(gopher.Link(gopher.TypeMenu, "Back to top", prefix+"/"))
		return m, nil
	}

	ix, err := index.Load(indexPath)
	if err != nil {
		return nil, err
	}

	terms := index.Tokenize(query)
	hits, total := ix.QueryN(terms, limit)

	var m gopher.Menu
	m.Add(gopher.Info("Search: " + query))
	m.Add(gopher.Info(text.Rule("=", text.Width)))
	switch {
	case total == 0:
		m.Add(gopher.Info("No documents matched."))
	case total > len(hits):
		m.Add(gopher.Info(fmt.Sprintf("%d matching documents; showing the best %d.", total, len(hits))))
	default:
		m.Add(gopher.Info(fmt.Sprintf("%d matching documents, best first.", total)))
	}
	m.Add(gopher.Info(""))

	for _, h := range hits {
		m.Add(gopher.Link(gopher.TypeText, text.Truncate(h.Doc.Title, gopher.MenuWidth), h.Doc.Selector))
		if s := snippet(root, prefix, h.Doc.Selector, terms); s != "" {
			m.Add(gopher.Info("      " + text.Truncate(s, gopher.MenuWidth-6)))
		}
		m.Add(gopher.Info(""))
	}
	m.Add(gopher.Link(gopher.TypeMenu, "Back to top", prefix+"/"))
	return m, nil
}

// snippet reads one matched document to quote a line from it. Only the
// results being shown are read, so this costs a handful of small files rather
// than the whole corpus.
func snippet(root, prefix, selector string, terms []string) string {
	rel := strings.TrimPrefix(strings.TrimPrefix(selector, prefix), "/")
	if rel == "" || strings.Contains(rel, "..") {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return ""
	}
	return search.Snippet(string(b), terms)
}

// searchQuery finds the terms Motsognir passed. QUERY_STRING_SEARCH holds the
// type-7 terms; QUERY_STRING carries whichever parameter was present, and a
// command-line argument makes the script testable by hand.
func searchQuery(args []string) string {
	for _, env := range []string{"QUERY_STRING_SEARCH", "QUERY_STRING"} {
		if v := os.Getenv(env); v != "" {
			// Motsognir passes the query undecoded, as CGI requires.
			if dec, err := url.QueryUnescape(v); err == nil {
				return dec
			}
			return v
		}
	}
	if len(args) > 0 {
		return strings.Join(args, " ")
	}
	return ""
}

// serverIdentity is what the emitted menu lines should advertise. Motsognir
// exports its own configured hostname and port, which is exactly right: the
// CGI does not need to know, and cannot guess, how it is reached.
func serverIdentity() (string, int) {
	host := os.Getenv("SERVER_NAME")
	if host == "" {
		host = "localhost"
	}
	port := 70
	if v := os.Getenv("SERVER_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			port = n
		}
	}
	return host, port
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
