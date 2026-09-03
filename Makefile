# Building from source needs Go 1.21 or newer (log/slog). The DEPLOYMENT HOST
# needs no Go at all: `make dist` cross-compiles a static binary, and
# `make deploy DEPLOY_HOST=user@host` ships it. Build here, not there.
GO      ?= go
GO_MIN  := 1.21
BIN     ?= bin
CONTENT ?= content
ADDR    ?= 127.0.0.1:7070
HOST    ?= localhost

# Cross-compilation target for the deployment host. EC2 is amd64 on Intel or
# AMD instance families and arm64 on Graviton; check with `uname -m` there.
DIST_OS   ?= linux
DIST_ARCH ?= amd64

# Deployment host for `make deploy`, e.g. DEPLOY_HOST=ec2-user@nrf.jonsharp.net
DEPLOY_HOST ?=

.PHONY: all check-go build test vet fmt ingest ingest-docs serve run dist deploy clean

all: build test

# Fail with an explanation rather than a wall of "undefined: slog".
check-go:
	@have=$$($(GO) env GOVERSION | sed 's/^go//'); \
	 lo=$$(printf '%s\n%s\n' "$(GO_MIN)" "$$have" | sort -V | head -1); \
	 if [ "$$lo" != "$(GO_MIN)" ]; then \
	     echo "This needs Go >= $(GO_MIN) (log/slog); found $$have." >&2; \
	     echo "" >&2; \
	     echo "If this is the deployment host, you do not need Go here at all:" >&2; \
	     echo "  build on your workstation with 'make dist' and copy the result," >&2; \
	     echo "  or run 'make deploy DEPLOY_HOST=user@host' from there." >&2; \
	     echo "" >&2; \
	     echo "To build here anyway, install a newer Go:" >&2; \
	     echo "  Amazon Linux 2023:  sudo dnf install -y golang" >&2; \
	     echo "  Debian/Ubuntu:      sudo snap install go --classic" >&2; \
	     echo "  any distribution:   https://go.dev/dl/ and put it first in PATH" >&2; \
	     exit 1; \
	 fi

build: check-go
	$(GO) build -o $(BIN)/nordicgopher ./cmd/nordicgopher
	$(GO) build -o $(BIN)/ngingest     ./cmd/ngingest
	$(GO) build -o $(BIN)/ngconv       ./cmd/ngconv

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

# Build the content tree, for local development.
#
# ON THE DEPLOYMENT HOST, USE THE UNIT INSTEAD:
#   sudo systemctl start nordicgopher-ingest.service
# These targets write to ./$(CONTENT) in the working copy, not to
# /var/lib/nordicgopher/content, so running them on the server produces a
# tree that nothing serves.
#
# Set GITHUB_TOKEN to raise the API budget from 60 to 5000 requests/hour.
# Without it, ngingest reads the remaining budget and mirrors as many
# repositories as it can afford rather than overspending and failing.
ingest: check-go
	$(GO) run ./cmd/ngingest -out $(CONTENT)

# The documentation ingest alone: one API request for the git tree, then the
# source files come from the CDN. This part needs no token.
ingest-docs: check-go
	$(GO) run ./cmd/ngingest -only ncsdocs -out $(CONTENT)

serve:
	$(GO) run ./cmd/nordicgopher -addr $(ADDR) -host $(HOST) -root $(CONTENT)

run: ingest serve

# Static binaries for the deployment host, plus the unit files to go with
# them. CGO is off so the result runs on any glibc or musl userland.
dist: check-go
	@mkdir -p dist
	CGO_ENABLED=0 GOOS=$(DIST_OS) GOARCH=$(DIST_ARCH) \
	    $(GO) build -trimpath -ldflags='-s -w' -o dist/nordicgopher ./cmd/nordicgopher
	CGO_ENABLED=0 GOOS=$(DIST_OS) GOARCH=$(DIST_ARCH) \
	    $(GO) build -trimpath -ldflags='-s -w' -o dist/ngingest     ./cmd/ngingest
	CGO_ENABLED=0 GOOS=$(DIST_OS) GOARCH=$(DIST_ARCH) \
	    $(GO) build -trimpath -ldflags='-s -w' -o dist/ngconv       ./cmd/ngconv
	cp deploy/*.service deploy/*.timer deploy/env.example deploy/install.sh dist/
	cp data/artifacts.json dist/
	@echo
	@echo "dist/ is ready for $(DIST_OS)/$(DIST_ARCH):"
	@ls -1 dist/

# Ship dist/ to the host and run the installer there. The host needs only
# ssh, tar and systemd -- no Go, no repository, no toolchain.
deploy: dist
	@test -n "$(DEPLOY_HOST)" || { \
	    echo "set DEPLOY_HOST, e.g. make deploy DEPLOY_HOST=ec2-user@nrf.jonsharp.net" >&2; \
	    exit 1; \
	}
	@echo "==> copying dist/ to $(DEPLOY_HOST)"
	tar czf - -C dist . | ssh $(DEPLOY_HOST) 'rm -rf /tmp/nordicgopher-dist && mkdir -p /tmp/nordicgopher-dist && tar xzf - -C /tmp/nordicgopher-dist'
	@echo "==> running installer on $(DEPLOY_HOST)"
	ssh -t $(DEPLOY_HOST) 'sudo sh /tmp/nordicgopher-dist/install.sh'

clean:
	rm -rf $(BIN) dist $(CONTENT) $(CONTENT).prev .cache
