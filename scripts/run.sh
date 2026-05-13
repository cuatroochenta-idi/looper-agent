#!/usr/bin/env bash
# Launcher for the Looper Agent examples and debug CLI.
#
#   ./scripts/run.sh                       → start the web UI on :9090
#   ./scripts/run.sh serve [--port 9090]   → start the web UI
#   ./scripts/run.sh example <N>           → run examples/0N_*
#   ./scripts/run.sh mcp                   → start the MCP debug server (stdio)
#   ./scripts/run.sh build                 → just build ./bin/looper
#   ./scripts/run.sh jaeger                → boot a local Jaeger all-in-one (OTLP :4317)
#   ./scripts/run.sh check                 → go build ./... + go vet ./...
#
# Always loads .env.local from the repo root if present.

set -euo pipefail

REPO_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

# ── 1. Load .env.local ────────────────────────────────────────────────────────
if [[ -f .env.local ]]; then
  set -a
  # shellcheck disable=SC1091
  source .env.local
  set +a
else
  echo "warn: no .env.local found at $REPO_ROOT" >&2
fi

# ── 2. Helpers ────────────────────────────────────────────────────────────────
build_cli() {
  mkdir -p bin
  echo "→ go build -o ./bin/looper ./cmd/looper"
  go build -o ./bin/looper ./cmd/looper
}

ensure_cli() {
  if [[ ! -x ./bin/looper ]]; then build_cli; fi
}

usage() {
  sed -n '1,12p' "$0" | sed 's/^# \{0,1\}//'
}

# ── 3. Dispatch ───────────────────────────────────────────────────────────────
cmd="${1:-serve}"
shift || true

case "$cmd" in
  -h | --help | help)
    usage
    ;;

  build)
    build_cli
    ;;

  check)
    echo "→ go build ./..." && go build ./...
    echo "→ go vet ./..."   && go vet ./...
    ;;

  serve)
    ensure_cli
    echo "→ ./bin/looper serve $*"
    echo "   open http://localhost:9090"
    exec ./bin/looper serve "$@"
    ;;

  mcp)
    ensure_cli
    exec ./bin/looper mcp
    ;;

  example)
    if [[ $# -lt 1 ]]; then
      echo "usage: $0 example <N>   (e.g. 04 or 4)" >&2
      ls examples/ | sed 's/^/   /'
      exit 1
    fi
    n="$1"; shift
    n="$(printf '%02d' "$((10#$n))")"
    target="$(ls -d "examples/${n}_"* 2>/dev/null | head -n1 || true)"
    if [[ -z "$target" ]]; then
      echo "no example matches examples/${n}_*" >&2
      ls examples/ | sed 's/^/   /'
      exit 1
    fi
    echo "→ go run ./$target $*"
    exec go run "./$target" "$@"
    ;;

  jaeger)
    echo "→ docker run jaegertracing/all-in-one (OTLP :4317, UI :16686)"
    exec docker run --rm \
      -p 4317:4317 -p 16686:16686 \
      -e COLLECTOR_OTLP_ENABLED=true \
      jaegertracing/all-in-one:latest
    ;;

  *)
    echo "unknown command: $cmd" >&2
    usage
    exit 1
    ;;
esac
