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

## Before you publicise it

The mirror labels itself unofficial on every page and cites the URL and
timestamp it came from. It is still a republication of Nordic material on a
personal domain by a Nordic employee, which will read as semi-official
whatever the footer says. Worth a word with whoever owns brand and legal
before submitting it to gopher directories — cheap now, awkward to unwind
after it is indexed.

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
