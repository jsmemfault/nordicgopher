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
	only := flag.String("only", "", "run a single ingester: github or files")
	verbose := flag.Bool("v", false, "verbose logging")
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	if err := run(log, *out, *cache, *catalog, *orgs, *only, *maxRepos, *maxReleases); err != nil {
		log.Error("ingest failed", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger, out, cache, catalog, orgs, only string, maxRepos, maxReleases int) error {
	now := time.Now()

	staging, err := os.MkdirTemp(filepath.Dir(absOr(out)), ".ngingest-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)

	t := tree.New(staging)
	hc := httpcache.New(cache)
	hc.Log = log

	var root gopher.Menu
	root.Add(banner()...)

	if only == "" || only == "github" {
		ing := &github.Ingester{
			HTTP: hc,
			Log:  log,
			Cfg: github.Config{
				Orgs:        splitList(orgs),
				MaxRepos:    maxRepos,
				MaxReleases: maxReleases,
				Token:       firstEnv("GITHUB_TOKEN", "GH_TOKEN"),
			},
		}
		frag, err := ing.Run(t, now)
		if err != nil {
			return fmt.Errorf("github: %w", err)
		}
		root.Add(frag...)
	}

	if only == "" || only == "files" {
		ing := &artifactory.Ingester{
			HTTP: hc,
			Log:  log,
			Cfg:  artifactory.Config{CatalogPath: catalog, Verify: true},
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
	log.Info("content tree written", "dir", out, "cache", hc.Stats())
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
