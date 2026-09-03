GO      ?= go
BIN     ?= bin
CONTENT ?= content
ADDR    ?= 127.0.0.1:7070
HOST    ?= localhost

# Cross-compilation target for the deployment host. EC2 is amd64 on Intel or
# AMD instance families and arm64 on Graviton; check with `uname -m` there.
DIST_OS   ?= linux
DIST_ARCH ?= amd64

.PHONY: all build test vet fmt ingest ingest-docs serve run dist clean

all: build test

build:
	$(GO) build -o $(BIN)/nordicgopher ./cmd/nordicgopher
	$(GO) build -o $(BIN)/ngingest     ./cmd/ngingest
	$(GO) build -o $(BIN)/ngconv       ./cmd/ngconv

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

# Build the content tree. Set GITHUB_TOKEN to raise the API rate limit from
# 60 to 5000 requests/hour, which is what a full run needs.
ingest:
	$(GO) run ./cmd/ngingest -out $(CONTENT)

# The documentation ingest alone. Needs no token: one API request for the git
# tree, then the source files come from the CDN.
ingest-docs:
	$(GO) run ./cmd/ngingest -only ncsdocs -out $(CONTENT)

serve:
	$(GO) run ./cmd/nordicgopher -addr $(ADDR) -host $(HOST) -root $(CONTENT)

run: ingest serve

# Static binaries for the deployment host, plus the unit files to go with
# them. CGO is off so the result runs on any glibc or musl userland.
dist:
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

clean:
	rm -rf $(BIN) dist $(CONTENT) $(CONTENT).prev .cache
