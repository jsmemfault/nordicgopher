// Command ngingest builds the content tree that nordicgopher serves.
//
// It generates into a staging directory and swaps the result into place, so a
// failed or partial run never replaces a working mirror. The previous tree is
// kept alongside as <dir>.prev for a manual rollback.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"nordicgopher/internal/gopher"
	"nordicgopher/internal/httpcache"
	"nordicgopher/internal/ingest/artifactory"
	"nordicgopher/internal/ingest/github"
	"nordicgopher/internal/ingest/ncsdocs"
	"nordicgopher/internal/text"
	"nordicgopher/internal/tree"
)

func main() {
	out := flag.String("out", "content", "content tree directory to produce")
	cache := flag.String("cache", ".cache", "HTTP cache directory")
	catalog := flag.String("catalog", "data/artifacts.json", "curated download catalogue")
	orgs := flag.String("orgs", "NordicSemiconductor,nrfconnect", "GitHub organisations to mirror")
	maxRepos := flag.Int("max-repos", 15, "repositories per organisation (0 = all)")
	maxReleases := flag.Int("max-releases", 8, "release notes per repository")
	only := flag.String("only", "", "run a single ingester: github, files or ncsdocs")
	docsRef := flag.String("docs-ref", "main", "sdk-nrf branch or tag to read documentation from")
	maxDocs := flag.Int("max-docs", 0, "documentation pages to mirror (0 = all reachable)")
	workers := flag.Int("workers", 8, "concurrent source fetches for documentation")
	verbose := flag.Bool("v", false, "verbose logging")
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	cfg := options{
		out: *out, cache: *cache, catalog: *catalog, orgs: *orgs, only: *only,
		maxRepos: *maxRepos, maxReleases: *maxReleases,
		docsRef: *docsRef, maxDocs: *maxDocs, workers: *workers,
	}
	if err := run(log, cfg); err != nil {
		log.Error("ingest failed", "err", err)
		os.Exit(1)
	}
}

// options carries the command line, so adding an ingester does not mean
// threading another parameter through run.
type options struct {
	out, cache, catalog, orgs, only string
	maxRepos, maxReleases           int
	docsRef                         string
	maxDocs, workers                int
}

func run(log *slog.Logger, opt options) error {
	now := time.Now()
	out, cache, only := opt.out, opt.cache, opt.only

	// Staging must share a parent with the destination so the swap is an
	// atomic rename rather than a copy.
	parent := filepath.Dir(absOr(out))
	cleanStaging(log, parent)
	staging, err := os.MkdirTemp(parent, ".ngingest-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)

	t := tree.New(staging)
	// The GitHub API is throttled; raw.githubusercontent.com is a CDN and is
	// not, so documentation source is fetched flat out through its own client
	// over the same cache directory.
	hc := httpcache.New(cache)
	hc.Log = log
	raw := httpcache.New(cache)
	raw.Log = log
	raw.MinGap = 0

	var root gopher.Menu
	root.Add(banner()...)

	if only == "" || only == "github" {
		ing := &github.Ingester{
			HTTP: hc,
			Log:  log,
			Cfg: github.Config{
				Orgs:        splitList(opt.orgs),
				MaxRepos:    opt.maxRepos,
				MaxReleases: opt.maxReleases,
				Token:       firstEnv("GITHUB_TOKEN", "GH_TOKEN"),
			},
		}
		frag, err := ing.Run(t, now)
		if err != nil {
			return fmt.Errorf("github: %w", err)
		}
		root.Add(frag...)
	}

	if only == "" || only == "ncsdocs" {
		ing := &ncsdocs.Ingester{
			HTTP: hc,
			Raw:  raw,
			Log:  log,
			Cfg: ncsdocs.Config{
				Repo:      "nrfconnect/sdk-nrf",
				Ref:       opt.docsRef,
				DocRoot:   "doc/nrf",
				Root:      "/ncs",
				Entry:     "index",
				Shortcuts: "shortcuts.txt",
				Links:     "links.txt",
				MaxDocs:   opt.maxDocs,
				Workers:   opt.workers,
				Token:     firstEnv("GITHUB_TOKEN", "GH_TOKEN"),
			},
		}
		frag, err := ing.Run(t, now)
		if err != nil {
			return fmt.Errorf("ncsdocs: %w", err)
		}
		root.Add(frag...)
	}

	if only == "" || only == "files" {
		ing := &artifactory.Ingester{
			HTTP: hc,
			Log:  log,
			Cfg:  artifactory.Config{CatalogPath: opt.catalog, Verify: true},
		}
		frag, err := ing.Run(t, now)
		if err != nil {
			return fmt.Errorf("artifactory: %w", err)
		}
		root.Add(frag...)
	}

	root.Add(gopher.Blank())
	root.Add(gopher.Link(gopher.TypeSearch, "Search this mirror", "/search"))
	root.Add(gopher.Link(gopher.TypeText, "About this mirror, and what it does not include", "/about.txt"))
	root.Add(gopher.Blank())
	root.Add(gopher.Info(text.Rule("-", text.Width)))
	root.Add(gopher.Info("Generated " + now.UTC().Format("2006-01-02 15:04 MST") +
		" - unofficial mirror, not affiliated with Nordic Semiconductor ASA"))

	if err := t.WriteMenu("/", root); err != nil {
		return err
	}
	if err := t.WriteFile("/about.txt", about(now)); err != nil {
		return err
	}

	if err := t.Swap(absOr(out)); err != nil {
		return err
	}
	log.Info("content tree written", "dir", out, "api", hc.Stats(), "raw", raw.Stats())
	return nil
}

func banner() gopher.Menu {
	var m gopher.Menu
	for _, l := range []string{
		"",
		"  nordicgopher",
		"  " + text.Rule("=", 60),
		"",
		"  A plain-text mirror of publicly available Nordic Semiconductor",
		"  resources, served over Gopher (RFC 1436).",
		"",
	} {
		m.Add(gopher.Info(l))
	}
	return m
}

func about(now time.Time) string {
	body := `WHAT THIS IS
============

An unofficial, plain-text mirror of publicly available Nordic
Semiconductor material, served over Gopher. Every page carries the URL it
was mirrored from and the time it was fetched; the canonical source is
always authoritative.

This mirror is not affiliated with, endorsed by, or operated by Nordic
Semiconductor ASA.

WHAT IS INCLUDED
================

  * The public GitHub organisations: repository metadata, README files
    and release notes, converted from Markdown and reStructuredText to
    fixed-width text.

  * The public file server at files.nordicsemi.com: a curated set of
    downloadable artifacts, streamed through this server as Gopher
    binary items, plus the repository index.

WHAT IS NOT INCLUDED, AND WHY
=============================

The main web properties - www.nordicsemi.com, docs.nordicsemi.com,
devzone.nordicsemi.com, academy.nordicsemi.com and infocenter - sit
behind a Cloudflare managed challenge that returns HTTP 403 to any
non-browser client. That applies to robots.txt and sitemap.xml as well,
so there is no documented crawl policy to follow and no polite-crawler
path to take. Mirroring those sources needs either an allowlisted
egress address or a content feed arranged with their owners, not a
workaround; until then they are absent rather than half-scraped.

Nothing behind authentication is mirrored, by design.

HOW TO NAVIGATE
===============

Menus are directories; text items are documents. Links inside a
document cannot be selectable, because a Gopher text file has no inline
link concept, so they appear as numbered markers with the targets
collected under "Links" at the end of the document.

The search item on the top-level menu matches words against every
mirrored document.
`
	return tree.Page("About this mirror", body, "https://www.nordicsemi.com/", now)
}

// cleanStaging removes staging directories left behind by a run that was
// killed before its own cleanup could happen. Without this they accumulate,
// each holding a full copy of the tree.
func cleanStaging(log *slog.Logger, parent string) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), ".ngingest-") {
			p := filepath.Join(parent, e.Name())
			log.Info("removing abandoned staging directory", "dir", p)
			os.RemoveAll(p)
		}
	}
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func firstEnv(names ...string) string {
	for _, n := range names {
		if v := os.Getenv(n); v != "" {
			return v
		}
	}
	return ""
}

func absOr(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}
