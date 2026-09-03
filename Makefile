GO      ?= go
BIN     ?= bin
CONTENT ?= content
ADDR    ?= 127.0.0.1:7070
HOST    ?= localhost

.PHONY: all build test vet fmt ingest serve run clean

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

serve:
	$(GO) run ./cmd/nordicgopher -addr $(ADDR) -host $(HOST) -root $(CONTENT)

run: ingest serve

clean:
	rm -rf $(BIN) $(CONTENT) $(CONTENT).prev .cache
