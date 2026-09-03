// Command nordicgopher serves a generated content tree over Gopher.
//
// The server is read-only and stateless: everything it publishes was written
// by ngingest. The two dynamic selectors are /search, which scans the tree,
// and /dl/, which streams allowlisted upstream downloads to clients that
// cannot follow an https link.
package main

import (
	"flag"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"nordicgopher/internal/gopher"
	"nordicgopher/internal/ingest/artifactory"
	"nordicgopher/internal/search"
)

func main() {
	addr := flag.String("addr", ":7070", "listen address (port 70 needs privileges)")
	root := flag.String("root", "content", "content tree to serve")
	host := flag.String("host", "localhost", "hostname to advertise in menu lines")
	port := flag.Int("port", 0, "port to advertise in menu lines (default: listen port)")
	prefix := flag.String("prefix", "",
		"selector prefix the tree was generated for, e.g. /nrf; stripped before lookup")
	searchSel := flag.String("search-selector", "",
		"selector that runs search; defaults to <prefix>/search. Point it at the tree's "+
			"search.cgi to serve a Motsognir-targeted tree unchanged")
	results := flag.Int("results", 50, "maximum search results")
	maxDL := flag.Int64("max-download", 512<<20, "cap on a single proxied download in bytes")
	maxConns := flag.Int("max-conns", gopher.DefaultMaxConns, "connections served at once")
	proxyDL := flag.Bool("proxy-downloads", true,
		"serve /dl/ by fetching from upstream; artifacts copied into the tree do not need it")
	proxyConc := flag.Int("proxy-concurrency", gopher.DefaultMaxConcurrent,
		"proxied downloads in flight at once")
	logClients := flag.Bool("log-clients", true, "record client addresses in the request log")
	verbose := flag.Bool("v", false, "verbose logging")
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	advertised := *port
	if advertised == 0 {
		advertised = portOf(*addr)
	}

	if _, err := os.Stat(*root); err != nil {
		log.Error("content tree not found; run ngingest first", "root", *root, "err", err)
		os.Exit(1)
	}

	srv := &gopher.Server{
		Root:       *root,
		Host:       *host,
		Port:       advertised,
		Log:        log,
		MaxConns:   *maxConns,
		LogClients: *logClients,
		Prefix:     strings.TrimSuffix(*prefix, "/"),
	}

	sel := *searchSel
	if sel == "" {
		sel = srv.Prefix + "/search"
	}
	log.Info("search mounted", "selector", sel)

	ix := &search.Index{Root: *root}
	srv.Handle(sel, func(w io.Writer, _, query string) error {
		m, err := ix.Query(query, *results)
		if err != nil {
			return err
		}
		return m.WriteTo(w, *host, advertised)
	})

	dlPrefix := srv.Prefix + "/dl/"
	if *proxyDL {
		proxy := &gopher.Proxy{
			// The only reachable upstream. files.nordicsemi.com is the one
			// public Nordic host that serves non-browser clients, and only
			// its artifactory path is exposed.
			Allow:         map[string]string{dlPrefix: artifactory.Base},
			MaxBytes:      *maxDL,
			MaxConcurrent: *proxyConc,
			UserAgent:     "nordicgopher/0.2 (gopher mirror)",
			HTTP:          &http.Client{Timeout: 30 * time.Minute},
			Log:           log,
		}
		srv.Handle(dlPrefix, proxy.Handle)
	} else {
		// Refuse rather than fall through to the content tree, where a /dl/
		// selector would report a confusing "not found".
		srv.Handle(dlPrefix, func(w io.Writer, _, _ string) error {
			return gopher.WriteError(w, "downloads are not proxied by this server")
		})
	}

	if err := srv.ListenAndServe(*addr); err != nil {
		log.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

func portOf(addr string) int {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return 70
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		return 70
	}
	return n
}
