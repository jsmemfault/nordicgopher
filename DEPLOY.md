# Deploying nordicgopher

Written for the target it was built for: an existing Linux EC2 host already
running Motsognir on port 70, with `nrf.jonsharp.net` CNAMEd to the
`jonsharp.net` address and this service on port 7070.

## Why 7070, and what that costs

Motsognir binds port 70 per-IP on that host, one hole per address. Rather
than allocate another Elastic IP for a third hole, this runs on 7070 behind
the existing `jonsharp.net` address. Port 7070 is unprivileged, so the
service needs no capabilities and never runs as root.

The cost is that **7070 is not the default port**, so every link has to carry
it:

```
gopher://nrf.jonsharp.net:7070/
```

Clients that assume port 70 will fail on a bare hostname. That matters when
submitting to gopher directories — check each one accepts a port — and it is
why `caps.txt` and the root menu exist: a visitor who arrives by hostname
alone gets nothing, so the link you publish must be complete.

## DNS

```
nrf.jonsharp.net.   CNAME   jonsharp.net.
```

A CNAME on a subdomain pointing at an apex is valid (the prohibition is on a
CNAME *at* an apex), and it follows the address if the instance's IP ever
changes. An A record to the same address works equally well.

Nothing about Gopher needs a TLS certificate — the protocol is cleartext by
design. Everything served here is already public, so there is nothing to
protect in transit, but do not add anything that isn't.

## Install

**The host needs no Go toolchain, and you should not build there.** The
binaries are statically linked with `CGO_ENABLED=0`, so they are built on
your workstation and copied over. Building from source needs Go 1.21 or newer
(`log/slog`), and a distribution's packaged Go is often older -- Amazon Linux
2 ships 1.20, which cannot compile this at all. The host needs only `ssh`,
`tar` and systemd.

### One command

From the repository on your workstation:

```sh
make deploy DEPLOY_HOST=ec2-user@nrf.jonsharp.net              # amd64
make deploy DEPLOY_HOST=ec2-user@nrf.jonsharp.net DIST_ARCH=arm64
```

That cross-compiles, copies `dist/` over, and runs the installer under
`sudo`. Then continue at [Configure](#configure).

### By hand

Build for the instance's architecture. Check which you need with `uname -m`
on the host: `x86_64` means amd64, `aarch64` means arm64 (Graviton).

```sh
make dist                      # linux/amd64, the default
make dist DIST_ARCH=arm64      # Graviton
```

Copy and install:

```sh
tar czf nordicgopher.tgz -C dist .
scp nordicgopher.tgz ec2-host:/tmp/
ssh ec2-host
  mkdir -p /tmp/ng && tar xzf /tmp/nordicgopher.tgz -C /tmp/ng
  sudo sh /tmp/ng/install.sh
```

`install.sh` creates a `nordicgopher` system user, installs the three
binaries and the unit files, and writes `/etc/nordicgopher/env` from the
template. It deliberately does not start anything, because the service
cannot work until that file is right. It is idempotent, so re-run it to
upgrade.

## Configure

Edit `/etc/nordicgopher/env`. One setting matters more than the rest:

```sh
PUBLIC_HOST=nrf.jonsharp.net
```

**Every menu line the server emits embeds this hostname and port.** Get it
wrong and the mirror looks perfect from the host itself while every link is
broken for everyone else, because clients follow the host in the menu line
rather than the one they connected to. It is the single most common way a
new gopherhole ships broken.

```sh
BIND_ADDRESS=<the jonsharp.net address>
```

Binding the specific address keeps 7070 free on the `frstcomputer.com`
address and matches Motsognir's one-hole-per-IP layout. `0.0.0.0` listens on
everything, which also works.

Set `ADMIN_CONTACT` — it is published in `caps.txt`, and a mirror of someone
else's content should say who to contact about it. `GITHUB_TOKEN` is
optional and only affects the repository ingest; see below.

## First run

```sh
sudo systemctl start nordicgopher-ingest.service
sudo journalctl -u nordicgopher-ingest.service -f
```

Two to three minutes cold. It ends with `content tree written`. Then:

```sh
sudo systemctl enable --now nordicgopher.service
sudo systemctl enable --now nordicgopher-ingest.timer
```

Verify from the host before opening the firewall, and read the host and port
in the output rather than just checking it responds:

```sh
printf '/\r\n' | nc localhost 7070 | head
```

Then open inbound TCP 7070 in the instance's security group, and in the host
firewall if one is running.

## What the nightly rebuild does

`nordicgopher-ingest.timer` runs daily with up to 30 minutes of jitter.
`ngingest` generates into a staging directory and renames it into place, so:

- the serving process never sees a partial tree,
- **no restart is needed** — it serves the new files immediately and reloads
  its search corpus when the root menu's timestamp changes,
- a failed run leaves the previous tree serving, and
- the previous tree stays as `content.prev` for a manual rollback.

Most content is unchanged between runs and the fetch cache revalidates with
ETags, so a typical night is a few hundred 304s.

### Rebuild on the host with the unit, not with make

```sh
sudo systemctl start nordicgopher-ingest.service
```

`make ingest` writes to `./content` in a working copy, not to
`/var/lib/nordicgopher/content`, so running it on the server builds a tree
that nothing serves. It also needs a Go toolchain the host does not need to
have.

### The token, and what it is for

Only the repository ingest needs `GITHUB_TOKEN`. The arithmetic is worth
knowing, because it decides whether an unauthenticated run can work at all:

| | requests |
| --- | --- |
| Unauthenticated budget, per hour, per IP | 60 |
| One organisation listing | 1 |
| One repository (README + releases) | 2 |
| The whole documentation ingest | **1** |

So two organisations at 15 repositories each costs 62 requests against a
budget of 60. `ngingest` now reads the remaining budget first — asking costs
nothing, as the `rate_limit` endpoint is not itself counted — and mirrors as
many repositories as it can afford, holding back a small reserve so the
documentation ingest is never starved of its single request. It says what it
did:

```
WARN  no GitHub token: the unauthenticated API allows 60 requests/hour
INFO  github API budget remaining=58 resets_in=58m0s
WARN  limiting repositories to fit the remaining API budget requested=15 using=13
```

If the budget is already spent it stops with what to do about it, rather than
failing partway through with a 403.

**Set a token.** With one the limit is 5000 requests an hour, `-max-repos 0`
mirrors all ~120 repositories, and none of the above applies. Put it in
`/etc/nordicgopher/env`; a fine-grained token with no scopes at all is
enough, since everything read here is public.

Two things make a first run on a cold cache more fragile than later ones, and
both are handled: throttled requests are retried with backoff, honouring
`Retry-After`, and the CDN fetches are paced (`-raw-gap`, 25 ms) because a
datacenter address pulling 889 files as fast as it can is the shape that gets
throttled, where the same run from a home connection is not. Raise the gap if
the host still gets throttled.

If the API throttles mid-run, the fetch cache falls back to its stored copy
rather than failing the rebuild, so a partial run republishes the previous
content for whatever it could not refresh. On a cold cache there is nothing
to fall back to, which is why the first run is the one to watch.

## Serving the tree from Motsognir instead

The generated tree is plain gophermap-and-text files, so an existing
Motsognir instance can serve it and the standalone server becomes optional.
The reason to do this is **port 70**: links stop carrying `:7070`, which is
what gets dropped when someone copies a link into a chat or a mailing list.

### What Gopher cannot do

There is no `Host` header in RFC 1436 — a client sends only a selector — so
Gopher has no name-based virtual hosting. Two hostnames on one address and
port are indistinguishable to the server. That is why `jonsharp.net` and
`frstcomputer.com` need separate addresses, and it means:

**`nrf.jonsharp.net` cannot be its own hole on port 70 of a shared address.**

Served from Motsognir, the mirror is a subdirectory of the existing hole:

```
gopher://jonsharp.net/1/nrf/
```

and `nrf.jonsharp.net` resolves to the same address and serves the personal
root menu. Keeping the dedicated hostname means keeping a dedicated port or a
dedicated address; there is no third option.

### The one dialect difference

Motsognir reads the **first character of every non-empty line** as the item
type. Bucktooth and Gophernicus infer `i` from a line having no tab;
Motsognir does not, so a bare banner line would be served as an item of type
`' '` or `'='`. The generator therefore writes informational lines with an
explicit `i` and a trailing tab, which is valid for all three.
`TestFormatIsMotsognirCompatible` reproduces Motsognir's parser to keep that
from regressing.

Otherwise the formats agree. When a line leaves server and port empty,
Motsognir fills in its configured `gopherhostname` and `gopherport`, which is
exactly what the generator relies on.

### Motsognir configuration

```
GopherCgiSupport=1
```

Search is a CGI, and without this Motsognir serves the script as a text file
instead of executing it.

### Generating and installing the tree

Generate it inside Motsognir's document root, with every selector carrying
the prefix:

```sh
ngingest \
    -out /var/gopher/nrf \
    -selector-prefix /nrf \
    -search-cgi search.cgi \
    -host jonsharp.net \
    -admin jon@jonsharp.net
```

Through the unit, put the same in `/etc/nordicgopher/env`:

```sh
CONTENT_DIR=/var/gopher/nrf
INGEST_EXTRA_ARGS=-selector-prefix /nrf -search-cgi search.cgi
```

and widen `ReadWritePaths` in `nordicgopher-ingest.service` to cover
`/var/gopher`, since `ProtectSystem=strict` otherwise makes it read-only.

Note the prefix applies to **selectors, not to the directory layout**: the
tree is self-contained and installs at `<document root>/nrf`. Baking the
prefix into the layout would nest it twice, and would drop a root
`gophermap` and an `about.txt` into the hosting hole's own directory —
overwriting its front page. For the same reason `caps.txt` and `robots.txt`
are not generated when a prefix is set: they belong at the root of the hole,
which is yours, not the mirror's. Add the mirror's paths to your own
`robots.txt` if you want crawlers to skip the copies.

Finally, link it from the hosting hole's root gophermap:

```
1nRF Connect SDK documentation (mirror)	/nrf/
```

### Search as a CGI

`ngingest` writes `search.cgi` into the tree itself, because its selector has
to be inside the tree and the tree is replaced wholesale on every ingest — a
hand-placed script would be deleted by the next run. The wrapper `exec`s
`ngsearch` with the paths that run produced.

A CGI starts fresh per query, so it cannot hold the corpus in memory the way
the standalone server does. Instead `ngingest` writes `search.idx`, an
inverted index (about 930 KB for 734 documents, 23,555 terms), and the CGI
reads that. Measured cost per query, cold process each time:

| | |
| --- | --- |
| Process start alone | 14 ms |
| + loading the index | 26 ms |
| + 50 result snippets | 30 ms |

which is slightly faster than the in-process server, whose advantage in
holding the corpus is offset by having to scan it.

One behavioural change comes with the index. The scan matched a term
**anywhere in a word**; the index matches **by token prefix**, since storing
every suffix would be far larger. So `nrf54` still finds `nrf54l15` and
`nrf54h20`, but `54l15` no longer finds `nrf54l15`. Identifiers are indexed
whole and also split on underscores, so `SB_CONFIG_NETCORE_EMPTY` is findable
by its full name and by `netcore`.

### Running both during a transition

One tree can be served by both daemons, so what you check on 7070 is
byte-identical to what Motsognir serves. Point the standalone server at the
prefixed tree:

```sh
nordicgopher -root /var/gopher/nrf -prefix /nrf \
             -search-selector /nrf/search.cgi \
             -host nrf.jonsharp.net -port 7070 -addr <address>:7070
```

`-prefix` strips the prefix before resolving a selector against the tree, and
`-search-selector` points the built-in search at the same selector Motsognir
executes the CGI for. Retire the standalone server once the CGI is proven.

## Public exposure

Three things are worth understanding before this is reachable from the
internet.

**Downloads are static, not proxied.** Artifacts are copied into the content
tree at ingest time and served from disk. This matters: a proxied download
selector turns every visitor into an outbound fetch from
`files.nordicsemi.com`, which makes the host an egress amplifier aimed at
Nordic's own file server. Anything larger than `-mirror-max-bytes` (64 MiB by
default) stays proxied, so if you add a large artifact to the catalogue,
either raise the limit or accept the proxy for it. With everything mirrored
you can turn the proxy off entirely:

```sh
-proxy-downloads=false
```

The proxy, when on, only ever reaches URLs under
`files.nordicsemi.com/artifactory/` — the allowlist keys on the full URL
prefix, not the hostname — and caps transfers in flight at
`-proxy-concurrency` (2).

**Connections are capped** at `-max-conns` (64). Gopher has no keep-alive, so
this bounds concurrent work rather than visitors, and it exists because each
search scans the whole corpus: a flood of queries is the cheapest way to make
a small instance unhappy. Over the cap, connections are shed with a type-3
`server busy` rather than queued.

**The request log records client addresses**, which on a public server is a
record of who read what. `-log-clients=false` turns that off and keeps the
rest of the log.

`robots.txt` is served and gopher crawlers do fetch it. It disallows the
documentation and repository trees on purpose: these are copies, and the
canonical pages should rank ahead of them.

## Provenance

Every page cites the URL it was mirrored from and the time it was fetched,
and `caps.txt` carries an administrator contact. Keep both accurate: the
mirror is a dated snapshot of someone else's documentation, and the value of
a copy depends on a reader being able to tell how old it is and where the
live version is.

Set `ADMIN_CONTACT` in `/etc/nordicgopher/env` before submitting the hole to
gopher directories — it is what they read to find out who runs it, and it
defaults to a placeholder that says it is unset.

## Operating

```sh
systemctl status nordicgopher
journalctl -u nordicgopher -f                  # request log
journalctl -u nordicgopher-ingest --since today
systemctl list-timers nordicgopher-ingest.timer

sudo systemctl start nordicgopher-ingest.service   # rebuild now
```

Rolling back a bad ingest:

```sh
cd /var/lib/nordicgopher
sudo -u nordicgopher mv content content.bad
sudo -u nordicgopher mv content.prev content
```

No restart needed; the next request serves the restored tree.
