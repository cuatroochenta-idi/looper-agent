# ==============================================================================
# Looper Agent — build, run, dev tooling
# ==============================================================================
#
# Quick start:
#   make tools          # one-time: install templ CLI if missing
#   make serve          # generate + build + run web panel on :9090
#
# Common dev loop:
#   make watch          # in one terminal: regenerate *_templ.go on save
#   make serve          # in another terminal: build & run
#
# See `make help` for the full list of targets.
# ==============================================================================

# ── Toolchain ────────────────────────────────────────────────────────────────
GO            ?= go
TEMPL         ?= templ
DOCKER        ?= docker

# ── Paths ────────────────────────────────────────────────────────────────────
BINARY        := looper
BIN_DIR       := bin
CMD_PATH      := ./cmd/looper
WEB_PKG       := ./internal/web
EXAMPLES_DIR  := ./examples

# ── Pinned versions ──────────────────────────────────────────────────────────
TEMPL_VERSION := v0.3.1020

# ── Runtime flags ────────────────────────────────────────────────────────────
PORT          ?= 9090
LDFLAGS       ?= -s -w
GOFLAGS       ?=

.DEFAULT_GOAL := help

# ── help ─────────────────────────────────────────────────────────────────────
.PHONY: help
help: ## Show available targets
	@printf "\nLooper Agent — Makefile targets\n\n"
	@awk 'BEGIN { FS = ":.*?## " } /^[a-zA-Z_.-]+:.*?## / { printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
	@printf "\nUse \`make example NAME=04_multi_provider\` to run a specific example.\n\n"

# ── Codegen ──────────────────────────────────────────────────────────────────
.PHONY: generate
generate: ## Compile *.templ → *_templ.go (idempotent)
	@$(TEMPL) generate $(WEB_PKG)

.PHONY: watch
watch: ## Watch *.templ files and regenerate on change (dev loop)
	@$(TEMPL) generate --watch $(WEB_PKG)

# ── Build ────────────────────────────────────────────────────────────────────
.PHONY: build
build: generate ## Generate templ + compile the CLI to bin/looper
	@mkdir -p $(BIN_DIR)
	$(GO) build $(GOFLAGS) -ldflags='$(LDFLAGS)' -o $(BIN_DIR)/$(BINARY) $(CMD_PATH)
	@echo "→ $(BIN_DIR)/$(BINARY) built"

.PHONY: build-all
build-all: generate ## Build everything in the module (smoke check)
	$(GO) build $(GOFLAGS) ./...

.PHONY: install
install: generate ## Install the CLI to $GOPATH/bin
	$(GO) install $(GOFLAGS) $(CMD_PATH)

# ── Run ──────────────────────────────────────────────────────────────────────
# `serve` and `example` autoload .env.local if it exists so API keys and
# LOOPER_OTEL_* env vars are picked up without manual sourcing.

define LOAD_ENV
@if [ -f .env.local ]; then set -a; . ./.env.local; set +a; fi
endef

.PHONY: serve
serve: build ## Build and run the web panel on :$(PORT)
	@if [ -f .env.local ]; then set -a; . ./.env.local; set +a; fi; \
	echo "→ http://localhost:$(PORT)"; \
	./$(BIN_DIR)/$(BINARY) serve --port $(PORT)

.PHONY: mcp
mcp: build ## Run the MCP debug server over stdio
	@if [ -f .env.local ]; then set -a; . ./.env.local; set +a; fi; \
	./$(BIN_DIR)/$(BINARY) mcp

# ── Examples ─────────────────────────────────────────────────────────────────
.PHONY: examples
examples: ## Compile every example (smoke build, no run)
	$(GO) build $(EXAMPLES_DIR)/...

.PHONY: example
example: ## Run a single example  ·  make example NAME=04_multi_provider
	@test -n "$(NAME)" || ( \
		echo "usage: make example NAME=<dir>" >&2; \
		echo "available:" >&2; \
		ls $(EXAMPLES_DIR) | grep '^[0-9]' | sed 's/^/  /' >&2; \
		exit 1 \
	)
	@if [ -f .env.local ]; then set -a; . ./.env.local; set +a; fi; \
	$(GO) run $(EXAMPLES_DIR)/$(NAME)

.PHONY: debug
debug: build ## Run an example with the looper panel as debugger  ·  make debug NAME=08_presentation_builder
	@test -n "$(NAME)" || ( \
		echo "usage: make debug NAME=<dir>" >&2; \
		echo "available:" >&2; \
		ls $(EXAMPLES_DIR) | grep '^[0-9]' | sed 's/^/  /' >&2; \
		exit 1 \
	)
	@if [ -f .env.local ]; then set -a; . ./.env.local; set +a; fi; \
	echo "→ looper panel  : http://localhost:$(PORT)"; \
	echo "→ example child : injected with LOOPER_TRACE_ENDPOINT"; \
	./$(BIN_DIR)/$(BINARY) serve --port $(PORT) -- $(GO) run $(EXAMPLES_DIR)/$(NAME)

# ── Quality ──────────────────────────────────────────────────────────────────
.PHONY: test
test: generate ## Run all unit tests
	$(GO) test $(GOFLAGS) ./...

.PHONY: test-race
test-race: generate ## Run tests with the race detector
	$(GO) test -race ./...

.PHONY: vet
vet: generate ## go vet ./...
	$(GO) vet ./...

.PHONY: fmt
fmt: ## Format Go + templ sources in-place
	$(GO) fmt ./...
	$(TEMPL) fmt $(WEB_PKG)

.PHONY: tidy
tidy: ## Sync go.mod / go.sum
	$(GO) mod tidy

.PHONY: check
check: vet test ## vet + test (the default CI gate)

# ── Tooling ──────────────────────────────────────────────────────────────────
.PHONY: tools
tools: ## Install dev tools (templ CLI) at pinned versions
	$(GO) install github.com/a-h/templ/cmd/templ@$(TEMPL_VERSION)
	@echo "→ tools installed under $(shell go env GOPATH)/bin"

.PHONY: tools-check
tools-check: ## Verify templ CLI matches the pinned version
	@got=$$($(TEMPL) version 2>&1 | tr -d 'v'); \
	want=$$(echo $(TEMPL_VERSION) | tr -d 'v'); \
	if [ "$$got" = "$$want" ]; then \
		echo "✓ templ $$got"; \
	else \
		echo "✗ templ version mismatch (have $$got, want $$want) — run 'make tools'" >&2; \
		exit 1; \
	fi

# ── OTel collector ───────────────────────────────────────────────────────────
.PHONY: jaeger
jaeger: ## Boot a local Jaeger all-in-one (OTLP :4317, UI :16686)
	@echo "→ Jaeger UI at http://localhost:16686"
	$(DOCKER) run --rm \
		-p 4317:4317 -p 16686:16686 \
		-e COLLECTOR_OTLP_ENABLED=true \
		jaegertracing/all-in-one:latest

# ── Cleanup ──────────────────────────────────────────────────────────────────
.PHONY: clean
clean: ## Remove the binary
	rm -rf $(BIN_DIR)

.PHONY: clean-all
clean-all: clean ## Remove the binary AND generated *_templ.go files
	@find $(WEB_PKG) -name '*_templ.go' -delete -print | sed 's/^/   ✗ /'

# ── Convenience ──────────────────────────────────────────────────────────────
.PHONY: deps
deps: tidy tools ## Sync go.mod + install dev tools

.PHONY: dev
dev: ## Quick reminder of the dev loop
	@printf "Terminal 1: %s\nTerminal 2: %s\n" "make watch" "make serve"
