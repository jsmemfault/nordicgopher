# nordicgopher

A plain-text mirror of publicly available Nordic Semiconductor resources,
served over Gopher (RFC 1436).

A working server, the content pipeline, and three ingesters -- including the
full nRF Connect SDK documentation, converted from its reStructuredText
source. It is unofficial and not affiliated with Nordic Semiconductor ASA.

## Quick start

```sh
make build
GITHUB_TOKEN=ghp_...  ./bin/ngingest -out content   # build the tree
./bin/nordicgopher -addr 127.0.0.1:7070 -host localhost -root content
```

Then point a client at `gopher://localhost:7070/`. Without a client to hand,
the protocol is a single line of request:

```sh
printf '/\r\n'                 | nc localhost 7070   # root menu
printf '/about.txt\r\n'        | nc localhost 7070   # a text item
printf '/search\tnrf9160\r\n'  | nc localhost 7070   # a type-7 query
```

`GITHUB_TOKEN` matters only for the repository ingest: the unauthenticated
GitHub API allows 60 requests per hour and each repository costs two, so
without a token use `-max-repos` to stay inside the budget. The documentation
ingest needs no token at all -- it spends exactly one API request on the
recursive git tree and then fetches 889 source files from
raw.githubusercontent.com, which is a CDN and not rate limited.

To build just the documentation:

```sh
./bin/ngingest -only ncsdocs -out content    # ~15s cold, ~7s cached
```

## What it mirrors, and what it cannot

Working sources:

| Source | Notes |
| --- | --- |
| `nrfconnect/sdk-nrf` `doc/nrf` | The nRF Connect SDK documentation, from RST source. 733 pages. **One API request** for the whole ingest. |
| `api.github.com` — `NordicSemiconductor`, `nrfconnect` | Open. Repository metadata, READMEs, release notes. |
| `files.nordicsemi.com` (Artifactory) | Repository index is anonymous; **deeper listing is not**. Downloads by known path are open. |

Everything under `nordicsemi.com` that a browser normally serves —
`www`, `docs`, `devzone`, `academy`, `infocenter`, `nrfconnectdocs` — sits
behind a Cloudflare managed challenge and returns **HTTP 403 to any
non-browser client**, `robots.txt` and `sitemap.xml` included. There is no
crawl policy to read and no polite-crawler path to take. Mirroring those
sources needs an allowlisted egress address or a content feed arranged with
their owners; it is not something to work around at the client end, so those
sources are absent rather than half-scraped.

The Artifactory restriction is narrower and worth stating precisely, because
it shapes the design: `api/repositories` and each repository root are
readable anonymously, `api/storage` below a root returns 404, and
`?list&deep=1` returns `403 available to authenticated users only`. Raw
`GET`s of known paths succeed. So the download tree is a curated catalogue
(`data/artifacts.json`), verified with a `HEAD` at ingest time, rather than a
crawl. Adding an authenticated token later would let the same ingester
enumerate properly.

## Architecture

```
ingesters   ->  converters  ->  content tree  ->  gopher server
  github         markdown        gophermap +       static tree
  ncsdocs        rst             text files        + /search
  artifactory                                      + /dl/ proxy
```

Generation and serving are separate on purpose. `ngingest` builds into a
staging directory and swaps it into place, so a failed run leaves the previous
mirror intact (the old tree is kept as `content.prev`). The server only ever
reads a directory, which also means the tree can be handed to Gophernicus
instead — the on-disk `gophermap` format is the Bucktooth/Gophernicus one.

### Packages

| Package | Responsibility |
| --- | --- |
| `internal/gopher` | Protocol: item types, menu encoding, `gophermap` parsing, TCP server, download proxy |
| `internal/text` | Markdown and reStructuredText to fixed-width text |
| `internal/tree` | Content-tree writer, page assembly, attribution |
| `internal/httpcache` | ETag-conditional fetch cache |
| `internal/ingest/*` | One package per source |
| `internal/ingest/ncsdocs` | The documentation ingest: toctree graph, label resolution |
| `internal/search` | Type-7 backend |

### The documentation ingest

Reading RST source rather than rendered HTML is what makes this tractable, and
it buys three things HTML cannot give:

- **The documentation's own hierarchy.** `toctree` directives define the
  structure, so the mirror's menus match the published navigation. Documents
  the entry point does not reach are orphans upstream too, and are skipped
  (103 of 836).
- **Working cross-references.** Every `.. _label:` is mapped to the selector
  of the document defining it (1916 labels), so `:ref:` in prose becomes a
  footnote pointing *into the mirror*, and each page's menu lists the pages it
  references as selectable items. This is the only way a cross-reference can
  be followed in gopherspace: a type-0 text file cannot carry a link.
- **Resolvable prose.** The Sphinx config appends `links.txt` and
  `shortcuts.txt` to every document via `rst_epilog`, so read in isolation a
  page is missing *words*, not just links -- "The |NCS| is a modern SDK"
  rather than "The nRF Connect SDK is a modern SDK". The ingester loads both
  tables (120 substitutions, 1531 hyperlink targets) and hands them to the
  converter.

Two constructs needed care. `.. include::` is expanded rather than dropped,
because the shared files carry real content (build steps, board tables); its
`:start-after:`/`:end-before:` options are honored, which is what makes the
self-include idiom in `security/ap_protect.rst` mean "reuse this passage"
instead of "expand this file again" -- ten self-includes at depth six is 10^6
expansions. Tables are emitted verbatim so their alignment survives, with role
and literal markup stripped from cells and the freed columns replaced by
spaces, since both wrappers are always longer than the text they wrap.

### Two things Gopher makes awkward

**Links.** A type-0 text file has no inline link concept. Links become
numbered markers with the targets collected under a `Links` heading at the end
of the document, and relative targets are resolved against the document's
upstream URL first — otherwise `see MIGRATION.md` points nowhere.

**Downloads.** A Gopher client cannot follow an `https` link, so a menu of
downloads is decoration unless the server fetches the bytes. `/dl/` does that,
restricted by an allowlist keyed on the full upstream URL prefix — not on
hostname — because without it the handler is an open proxy. Traversal,
percent-encoded traversal, and scheme-relative escapes are all rejected;
`internal/gopher/gopher_test.go` covers each.

## Tools

- `ngingest` — build the content tree.
- `nordicgopher` — serve it.
- `ngconv` — convert one Markdown or RST file to text and print it. This is
  the fastest way to see a conversion bug:
  `./bin/ngconv -base https://github.com/org/repo/blob/main/README.md README.md`

## Known limitations

- Markdown inside `<details>` blocks is indented in the source and is emitted
  as a code block, so raw markup shows through. Uncommon in documentation,
  common in changelogs.
- Search holds the corpus in memory and scans it per query -- about 37 ms
  across 12 MB. Still a scan, not an index; the intended replacement is an
  SQLite FTS5 index built during ingest.
- Wide tables stay wide. A 160-column grid table is 160 columns in the mirror
  too, because narrowing it would destroy the alignment that makes it a table.
- Substitutions inside table cells are left unexpanded (38 pages), for the
  same reason: the expansion is longer than the reference, so there is no room
  for it without shifting every separator.
- Includes that reach into Zephyr's documentation tree cannot be resolved from
  this repository and are marked in place (4 pages). Sphinx resolves them
  through a mapping in `conf.py`; mirroring Zephyr's docs would fix it.
- The RST converter handles the constructs Nordic and Zephyr docs use and
  drops the rest. It is not docutils.
- Nothing behind authentication is mirrored, by design.

## Next

1. **Zephyr documentation**, which would resolve the cross-tree includes and
   the `:zephyr:` roles that currently lose their targets.
   `docs.zephyrproject.org` serves `objects.inv` to plain clients, and the
   source is on GitHub; the ingester generalises with little more than a
   second `Config`.
2. **FTS5 search index** built at ingest time.
3. **nrfxlib, MCUboot and TF-M docs**, which live in sibling `doc/` trees in
   the same repository and reuse the ingester as-is.
4. **The gated web properties**, once access is settled.
5. **Gemini** over the same tree — nearly free once the content model exists,
   and its link handling suits documentation better than Gopher's.

## Provenance and attribution

Every generated page states the URL it was mirrored from and the time it was
fetched, and says it is unofficial. A Gopher mirror of Nordic content reads as
official whether or not it claims to be, so this is built in from the start
rather than retrofitted. Before anything goes public-facing, the branding and
licensing question is worth settling with whoever owns it.
