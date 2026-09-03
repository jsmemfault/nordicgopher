# nordicgopher

A plain-text mirror of publicly available Nordic Semiconductor resources,
served over Gopher (RFC 1436).

This is phase one: a working server, the content pipeline, and two ingesters.
It is unofficial and not affiliated with Nordic Semiconductor ASA.

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

`GITHUB_TOKEN` is optional but effectively required for a full run: the
unauthenticated GitHub API allows 60 requests per hour, and each repository
costs two. Without one, use `-max-repos` to stay inside the budget.

## What it mirrors, and what it cannot

Working sources:

| Source | Notes |
| --- | --- |
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
ingesters  ->  converters  ->  content tree  ->  gopher server
  github        markdown        gophermap +       static tree
  artifactory   rst             text files        + /search
                                                  + /dl/ proxy
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
| `internal/search` | Type-7 backend |

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
- Search scans every text file per query. Fine for a phase-one tree; the
  intended replacement is an SQLite FTS5 index built during ingest.
- The RST converter handles the constructs Nordic and Zephyr docs use and
  drops the rest. It is not docutils. Tables in particular pass through
  roughly.
- Nothing behind authentication is mirrored, by design.

## Next

1. **nRF Connect SDK and Zephyr documentation from RST source** —
   `nrfconnect/sdk-nrf/doc` is open on GitHub, and `docs.zephyrproject.org`
   serves `objects.inv` to plain clients. This is the bulk of the value and
   reuses `internal/text` as-is.
2. **FTS5 search index** built at ingest time.
3. **The gated web properties**, once access is settled.
4. **Gemini** over the same tree — nearly free once the content model exists,
   and its link handling suits documentation better than Gopher's.

## Provenance and attribution

Every generated page states the URL it was mirrored from and the time it was
fetched, and says it is unofficial. A Gopher mirror of Nordic content reads as
official whether or not it claims to be, so this is built in from the start
rather than retrofitted. Before anything goes public-facing, the branding and
licensing question is worth settling with whoever owns it.
