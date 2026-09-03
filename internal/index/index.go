// Package index builds and reads the search index.
//
// The server can afford to hold the whole corpus in memory and scan it, but a
// CGI cannot: it starts fresh for every query, so whatever it needs must be
// cheap to load. This package writes an inverted index at ingest time so a
// query is a lookup rather than a scan, which is what lets the mirror be
// served by an existing Motsognir instance with search intact.
//
// Matching is by token prefix rather than substring. The scan it replaces
// matched anywhere in a word, which an inverted index cannot do without
// storing every suffix; prefix matching recovers the case that actually
// matters in these documents -- "nrf54" finding "nrf54l15" and "nrf54h20" --
// at the cost of no longer matching a word's interior.
package index

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// magic identifies the file format; the trailing digit is the version.
const magic = "NGIX1\n"

// MaxFileSize caps how much of a document is indexed.
const MaxFileSize = 1 << 20

// Doc is one indexed document.
type Doc struct {
	Selector string // gopher selector, including any prefix
	Title    string
}

// Posting records that a term occurs in a document, and how often.
type Posting struct {
	Doc  uint32
	Freq uint32
}

// Index is an inverted index over a content tree.
type Index struct {
	Docs []Doc

	// terms is sorted, and postings is parallel to it. Sorted order is what
	// makes prefix matching a range scan rather than a full pass.
	terms    []string
	postings [][]Posting
}

// Result is one scored match.
type Result struct {
	Doc   Doc
	Score int
}

// --- building --------------------------------------------------------------

// Build indexes every .txt file under root. Selectors are formed from the
// path relative to root, with prefix prepended, so the index records the
// selectors as they will be served.
func Build(root, prefix string) (*Index, error) {
	prefix = strings.TrimSuffix(prefix, "/")

	var docs []Doc
	freqs := map[string]map[uint32]uint32{}

	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".txt") {
			return nil
		}
		// caps.txt and robots.txt are server metadata, not content.
		switch filepath.Base(p) {
		case "caps.txt", "robots.txt":
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > MaxFileSize {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}

		body := string(b)
		title := ""
		if i := strings.IndexByte(body, '\n'); i >= 0 {
			title = strings.TrimSpace(body[:i])
		}
		id := uint32(len(docs))
		docs = append(docs, Doc{
			Selector: prefix + "/" + filepath.ToSlash(rel),
			Title:    title,
		})
		for _, tok := range Tokenize(body) {
			m := freqs[tok]
			if m == nil {
				m = map[uint32]uint32{}
				freqs[tok] = m
			}
			m[id]++
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	ix := &Index{Docs: docs}
	ix.terms = make([]string, 0, len(freqs))
	for t := range freqs {
		ix.terms = append(ix.terms, t)
	}
	sort.Strings(ix.terms)
	ix.postings = make([][]Posting, len(ix.terms))
	for i, t := range ix.terms {
		pl := make([]Posting, 0, len(freqs[t]))
		for doc, n := range freqs[t] {
			pl = append(pl, Posting{Doc: doc, Freq: n})
		}
		sort.Slice(pl, func(a, b int) bool { return pl[a].Doc < pl[b].Doc })
		ix.postings[i] = pl
	}
	return ix, nil
}

// Tokenize splits text into lowercased index terms.
//
// Identifiers are kept whole and also split on underscores, so
// SB_CONFIG_NETCORE_EMPTY is findable both by its full name and by "netcore".
// Without the split, searching for a component of a Kconfig symbol -- which
// is how people actually remember them -- would find nothing.
func Tokenize(text string) []string {
	var out []string
	var b strings.Builder
	flush := func() {
		if b.Len() == 0 {
			return
		}
		tok := b.String()
		b.Reset()
		if len(tok) < 2 || len(tok) > 40 {
			return
		}
		out = append(out, tok)
		if strings.Contains(tok, "_") {
			for _, part := range strings.Split(tok, "_") {
				if len(part) >= 2 && len(part) <= 40 {
					out = append(out, part)
				}
			}
		}
	}
	for _, r := range strings.ToLower(text) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	return out
}

// --- querying --------------------------------------------------------------

// Query returns documents matching every term, best first. A term matches any
// indexed token it is a prefix of. Occurrences in a title count extra: a
// document about a term should beat one that merely mentions it.
func (ix *Index) Query(terms []string, limit int) []Result {
	if len(terms) == 0 {
		return nil
	}

	scores := map[uint32]int{}
	for i, term := range terms {
		hits := ix.matchPrefix(term)
		if len(hits) == 0 {
			// Every term must match something, as the scan it replaces
			// required.
			return nil
		}
		if i == 0 {
			for doc, n := range hits {
				scores[doc] = n
			}
			continue
		}
		for doc := range scores {
			if n, ok := hits[doc]; ok {
				scores[doc] += n
			} else {
				delete(scores, doc)
			}
		}
		if len(scores) == 0 {
			return nil
		}
	}

	out := make([]Result, 0, len(scores))
	for doc, score := range scores {
		d := ix.Docs[doc]
		lowTitle := strings.ToLower(d.Title)
		for _, term := range terms {
			if strings.Contains(lowTitle, term) {
				score += 25
			}
		}
		out = append(out, Result{Doc: d, Score: score})
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Score != out[b].Score {
			return out[a].Score > out[b].Score
		}
		return out[a].Doc.Selector < out[b].Doc.Selector
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// QueryN returns the best results up to limit, and how many matched in all.
// Callers that want both should use this rather than querying twice: the
// match and scoring work is the expensive part, and doing it again to count
// the results doubles the cost of every search.
func (ix *Index) QueryN(terms []string, limit int) (hits []Result, total int) {
	all := ix.Query(terms, 0)
	total = len(all)
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, total
}

// matchPrefix accumulates the postings of every term with the given prefix.
func (ix *Index) matchPrefix(prefix string) map[uint32]int {
	i := sort.SearchStrings(ix.terms, prefix)
	out := map[uint32]int{}
	for ; i < len(ix.terms) && strings.HasPrefix(ix.terms[i], prefix); i++ {
		for _, p := range ix.postings[i] {
			out[p.Doc] += int(p.Freq)
		}
	}
	return out
}

// Terms reports the vocabulary size, for logging.
func (ix *Index) Terms() int { return len(ix.terms) }

// --- serialising -----------------------------------------------------------

// WriteFile writes the index. Document ids are implicit in their order, and
// posting lists are delta-encoded, which keeps the file small enough that a
// CGI can read all of it per query.
func (ix *Index) WriteFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	w := bufio.NewWriterSize(f, 1<<16)
	w.WriteString(magic)

	putUvarint(w, uint64(len(ix.Docs)))
	for _, d := range ix.Docs {
		putString(w, d.Selector)
		putString(w, d.Title)
	}

	putUvarint(w, uint64(len(ix.terms)))
	for i, t := range ix.terms {
		putString(w, t)
		pl := ix.postings[i]
		putUvarint(w, uint64(len(pl)))
		var prev uint32
		for _, p := range pl {
			putUvarint(w, uint64(p.Doc-prev))
			putUvarint(w, uint64(p.Freq))
			prev = p.Doc
		}
	}
	return w.Flush()
}

// Load reads an index written by WriteFile.
func Load(path string) (*Index, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<16)

	head := make([]byte, len(magic))
	if _, err := io.ReadFull(r, head); err != nil {
		return nil, fmt.Errorf("reading index header: %w", err)
	}
	if string(head) != magic {
		return nil, fmt.Errorf("%s is not a nordicgopher index (bad magic)", path)
	}

	nDocs, err := binary.ReadUvarint(r)
	if err != nil {
		return nil, err
	}
	ix := &Index{Docs: make([]Doc, nDocs)}
	for i := range ix.Docs {
		if ix.Docs[i].Selector, err = readString(r); err != nil {
			return nil, err
		}
		if ix.Docs[i].Title, err = readString(r); err != nil {
			return nil, err
		}
	}

	nTerms, err := binary.ReadUvarint(r)
	if err != nil {
		return nil, err
	}
	ix.terms = make([]string, nTerms)
	ix.postings = make([][]Posting, nTerms)
	for i := range ix.terms {
		if ix.terms[i], err = readString(r); err != nil {
			return nil, err
		}
		n, err := binary.ReadUvarint(r)
		if err != nil {
			return nil, err
		}
		pl := make([]Posting, n)
		var prev uint32
		for j := range pl {
			delta, err := binary.ReadUvarint(r)
			if err != nil {
				return nil, err
			}
			freq, err := binary.ReadUvarint(r)
			if err != nil {
				return nil, err
			}
			pl[j] = Posting{Doc: prev + uint32(delta), Freq: uint32(freq)}
			prev = pl[j].Doc
		}
		ix.postings[i] = pl
	}
	return ix, nil
}

func putUvarint(w *bufio.Writer, v uint64) {
	var buf [binary.MaxVarintLen64]byte
	w.Write(buf[:binary.PutUvarint(buf[:], v)])
}

func putString(w *bufio.Writer, s string) {
	putUvarint(w, uint64(len(s)))
	w.WriteString(s)
}

func readString(r *bufio.Reader) (string, error) {
	n, err := binary.ReadUvarint(r)
	if err != nil {
		return "", err
	}
	if n > 1<<20 {
		return "", fmt.Errorf("implausible string length %d in index", n)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	return string(b), nil
}
