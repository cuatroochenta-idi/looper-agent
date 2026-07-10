# ==============================================================================
# Looper Agent — build, run, dev tooling
# ==============================================================================
#
# Quick start:
#   make serve          # build + run web panel on :9090 (placeholder UI)
#   make release        # build the real SolidJS UI in, then compile the CLI
#   make ui-dev         # live Vite dev server for iterating on the UI
#
# The panel UI (internal/web/ui/dist) is embedded at build time via //go:embed.
# The built SPA bundle is committed at release time so `go install` / module
# consumers get the real UI; between releases a placeholder keeps plain builds
# working WITHOUT the JS toolchain (Bun); the real SolidJS SPA bundle is built
# separately from ui/ by `make ui-build` and folded into the binary by
# `make release`. Plain `build` never depends on Bun.
#
# See `make help` for the full list of targets.
# ==============================================================================

# ── Toolchain ────────────────────────────────────────────────────────────────
GO            ?= go
DOCKER        ?= docker
BUN           ?= bun

# ── Paths ────────────────────────────────────────────────────────────────────
BINARY        := looper
BIN_DIR       := bin
CMD_PATH      := ./cmd/looper
WEB_PKG       := ./internal/web
EXAMPLES_DIR  := ./examples
UI_DIR        := ui
UI_DIST       := ui/dist
WEB_DIST      := internal/web/ui/dist

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

# ── Build ────────────────────────────────────────────────────────────────────
.PHONY: build
build: ## Compile the CLI to bin/looper
	@mkdir -p $(BIN_DIR)
	$(GO) build $(GOFLAGS) -ldflags='$(LDFLAGS)' -o $(BIN_DIR)/$(BINARY) $(CMD_PATH)
	@echo "→ $(BIN_DIR)/$(BINARY) built"

.PHONY: build-all
build-all: ## Build everything in the module (smoke check)
	$(GO) build $(GOFLAGS) ./...

.PHONY: install
install: ## Install the CLI to $GOPATH/bin
	$(GO) install $(GOFLAGS) $(CMD_PATH)

# ── UI (Bun) ─────────────────────────────────────────────────────────────────
# The SolidJS panel lives in ui/. Bun is the ONLY supported package manager /
# task runner here — never npm/pnpm. These targets are the bridge that folds the
# built SPA into internal/web/ui/dist so the Go binary can embed it.

.PHONY: ui-install
ui-install: ## Install the UI dependencies with Bun
	cd $(UI_DIR) && $(BUN) install

.PHONY: ui-build
ui-build: ui-install ## Build the SolidJS SPA and sync it into internal/web/ui/dist
	cd $(UI_DIR) && $(BUN) run build
	@rm -rf $(WEB_DIST)
	@mkdir -p $(WEB_DIST)
	cp -R $(UI_DIST)/. $(WEB_DIST)/
	@echo "→ synced $(UI_DIST)/ → $(WEB_DIST)/ (commit at release so go-install users get the real UI)"

.PHONY: ui-dev
ui-dev: ## Run the Vite dev server (proxies /api,/ingest to :$(PORT) — start `make serve` too)
	cd $(UI_DIR) && $(BUN) run dev

.PHONY: ui-clean
ui-clean: ## Restore the committed placeholder in internal/web/ui/dist
	git checkout -- $(WEB_DIST)

.PHONY: release
release: ui-build build ## Full CLI bundle with the real UI embedded (ui-build + build)

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
test: ## Run all unit tests
	$(GO) test $(GOFLAGS) ./...

.PHONY: test-race
test-race: ## Run tests with the race detector
	$(GO) test -race ./...

.PHONY: vet
vet: ## go vet ./...
	$(GO) vet ./...

.PHONY: fmt
fmt: ## Format Go sources in-place
	$(GO) fmt ./...

.PHONY: tidy
tidy: ## Sync go.mod / go.sum
	$(GO) mod tidy

.PHONY: check
check: vet test ## vet + test (the default CI gate)

# ── Database migrations (Atlas) ──────────────────────────────────────────────
ATLAS         ?= atlas
PG_MIGRATIONS := internal/store/postgres/migrations
PG_SCHEMA     := internal/store/postgres/schema.sql

.PHONY: db-diff
db-diff: ## Author a new Postgres migration from schema.sql (needs atlas + docker)
	@if [ -z "$(NAME)" ]; then echo "usage: make db-diff NAME=<migration_name>" >&2; exit 1; fi
	$(ATLAS) migrate diff $(NAME) \
		--dir "file://$(PG_MIGRATIONS)" \
		--to "file://$(PG_SCHEMA)" \
		--dev-url "docker://postgres/17/dev"

.PHONY: db-hash
db-hash: ## Recompute atlas.sum after hand-editing a migration
	$(ATLAS) migrate hash --dir "file://$(PG_MIGRATIONS)"

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

# ── Convenience ──────────────────────────────────────────────────────────────
.PHONY: deps
deps: tidy ## Sync go.mod / go.sum
