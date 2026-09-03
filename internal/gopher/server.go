package gopher

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Handler serves a selector that is not backed by a file in the content tree.
// query carries the search terms a client sends after a tab on type-7 items.
type Handler func(w io.Writer, selector, query string) error

// Server serves a content tree over Gopher, with optional dynamic handlers
// mounted at selector prefixes.
type Server struct {
	Root string // directory holding the generated content tree
	Host string // hostname to advertise in menu lines
	Port int    // port to advertise in menu lines
	Log  *slog.Logger

	// Prefix is the selector prefix the tree was generated for, e.g.
	// "/nrf". It is stripped before a selector is resolved against Root, so
	// a tree built to be served as a subdirectory of another gopherhole can
	// also be served directly by this server. That lets one generated tree
	// be checked here and served there, rather than maintaining two.
	Prefix string

	// MaxConns bounds connections served at once. Gopher has no keep-alive,
	// so this is a cap on concurrent work rather than on visitors: it stops
	// a flood of search queries, each of which scans the whole corpus, from
	// exhausting a small host. Zero means DefaultMaxConns.
	MaxConns int

	// LogClients records the client address on each request. A public
	// server's request log is a record of who read what, so this is worth
	// being able to turn off.
	LogClients bool

	handlers []mount
	sem      chan struct{}
	once     sync.Once
}

// DefaultMaxConns is the connection limit when none is configured.
const DefaultMaxConns = 64

type mount struct {
	prefix string
	h      Handler
}

// Handle mounts a dynamic handler at a selector prefix. Longer prefixes win.
func (s *Server) Handle(prefix string, h Handler) {
	s.handlers = append(s.handlers, mount{prefix, h})
	sort.Slice(s.handlers, func(i, j int) bool {
		return len(s.handlers[i].prefix) > len(s.handlers[j].prefix)
	})
}

func (s *Server) logger() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// ListenAndServe accepts connections until the listener fails.
func (s *Server) ListenAndServe(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	s.init()
	s.logger().Info("listening",
		"addr", addr, "root", s.Root,
		"advertise", fmt.Sprintf("%s:%d", s.Host, s.Port),
		"max_conns", cap(s.sem))
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go s.serve(conn)
	}
}

func (s *Server) init() {
	s.once.Do(func() {
		n := s.MaxConns
		if n <= 0 {
			n = DefaultMaxConns
		}
		s.sem = make(chan struct{}, n)
	})
}

// requestLimit caps the selector line. Gopher has no length field, so an
// unbounded read is a trivial memory-exhaustion vector.
const requestLimit = 4096

func (s *Server) serve(conn net.Conn) {
	defer conn.Close()
	s.init()

	// Shed load rather than queue it: a client that waits behind a full
	// backlog will time out anyway, and telling it so costs one line.
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	default:
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		WriteError(conn, "server busy, please try again")
		s.logger().Warn("connection refused: at capacity", "max_conns", cap(s.sem))
		return
	}

	conn.SetDeadline(time.Now().Add(10 * time.Minute))

	line, err := bufio.NewReaderSize(conn, requestLimit).ReadString('\n')
	if err != nil && line == "" {
		return
	}
	line = strings.TrimRight(line, "\r\n")

	selector, query, _ := strings.Cut(line, "\t")
	if selector == "" {
		selector = "/"
	}
	if !strings.HasPrefix(selector, "/") {
		selector = "/" + selector
	}

	start := time.Now()
	err = s.route(conn, selector, query)

	attrs := []any{
		"selector", selector, "query", query,
		"dur", time.Since(start).Round(time.Millisecond),
		"err", err,
	}
	if s.LogClients {
		attrs = append(attrs, "remote", conn.RemoteAddr().String())
	}
	s.logger().Info("request", attrs...)
}

func (s *Server) route(w io.Writer, selector, query string) error {
	for _, m := range s.handlers {
		if strings.HasPrefix(selector, m.prefix) {
			if err := m.h(w, selector, query); err != nil {
				WriteError(w, "error: "+err.Error())
				return err
			}
			return nil
		}
	}
	return s.serveTree(w, selector)
}

// resolve maps a selector to a path inside Root, refusing anything that
// escapes the tree. path.Clean on an absolute selector collapses "..", and
// the prefix check catches symlink-free escapes that survive it.
func (s *Server) resolve(selector string) (string, error) {
	root, err := filepath.Abs(s.Root)
	if err != nil {
		return "", err
	}
	if p := strings.TrimSuffix(s.Prefix, "/"); p != "" {
		switch {
		case selector == p:
			selector = "/"
		case strings.HasPrefix(selector, p+"/"):
			selector = strings.TrimPrefix(selector, p)
		}
	}
	rel := path.Clean("/" + strings.TrimPrefix(selector, "/"))
	full := filepath.Join(root, filepath.FromSlash(rel))
	if full != root && !strings.HasPrefix(full, root+string(os.PathSeparator)) {
		return "", errors.New("selector outside content root")
	}
	return full, nil
}

func (s *Server) serveTree(w io.Writer, selector string) error {
	full, err := s.resolve(selector)
	if err != nil {
		WriteError(w, "bad selector")
		return err
	}
	fi, err := os.Stat(full)
	if err != nil {
		WriteError(w, "not found: "+selector)
		return nil
	}
	if fi.IsDir() {
		return s.serveDir(w, selector, full)
	}
	return s.serveFile(w, full)
}

func (s *Server) serveDir(w io.Writer, selector, dir string) error {
	if f, err := os.Open(filepath.Join(dir, "gophermap")); err == nil {
		defer f.Close()
		m, err := ParseGophermap(f)
		if err != nil {
			return err
		}
		return m.WriteTo(w, s.Host, s.Port)
	}
	// No gophermap: synthesise a listing so a partially generated tree is
	// still navigable.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var m Menu
	m.Add(Info(strings.TrimSuffix(selector, "/")+"/"), Blank())
	for _, e := range entries {
		if e.Name() == "gophermap" || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		sel := path.Join(selector, e.Name())
		if e.IsDir() {
			m.Add(Link(TypeMenu, e.Name()+"/", sel+"/"))
			continue
		}
		m.Add(Link(TypeForFile(e.Name()), e.Name(), sel))
	}
	return m.WriteTo(w, s.Host, s.Port)
}

func (s *Server) serveFile(w io.Writer, full string) error {
	f, err := os.Open(full)
	if err != nil {
		WriteError(w, "cannot open file")
		return err
	}
	defer f.Close()
	if TypeForFile(full) == TypeText {
		return WriteText(w, f)
	}
	_, err = io.Copy(w, f)
	return err
}

// TypeForFile guesses an item type from a filename extension.
//
// An unrecognised or absent extension is treated as binary, which is the
// conservative direction: a text file served as binary merely arrives without
// dot-termination and still displays, whereas a binary served as text is
// corrupted by the line-ending rewrite and the dot-stuffing. It also matters
// concretely here, because mirrored Unix executables have no extension at
// all. Everything the ingesters generate as text is named ".txt".
func TypeForFile(name string) Type {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".txt", ".md", ".rst", ".c", ".h", ".py", ".conf", ".cfg", ".json", ".yml", ".yaml", ".dts", ".overlay":
		return TypeText
	case ".gif":
		return TypeGIF
	case ".png", ".jpg", ".jpeg", ".svg", ".webp":
		return TypeImage
	case ".zip", ".tar", ".gz", ".tgz", ".bz2", ".xz", ".7z":
		return TypeArchive
	default:
		return TypeBinary
	}
}
