# Illustrative multi-stage build for the server-panel example.
# Build context is the REPO ROOT:
#   docker build -f examples/20_server_panel/Dockerfile -t looper-panel .
# Not wired into CI — a correct starting point you adapt to your pipeline.

# ── Stage 1: build the SolidJS UI with Bun ────────────────────────────────────
FROM oven/bun:1 AS ui
WORKDIR /ui
# Copy manifests first so the dep layer caches across UI source changes.
COPY ui/package.json ui/bun.lock ./
RUN bun install --frozen-lockfile
COPY ui/ ./
RUN bun run build            # → /ui/dist

# ── Stage 2: build the Go binary with the UI embedded ─────────────────────────
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Drop the built SPA into the embed path so //go:embed picks up the real UI
# instead of the committed placeholder.
RUN rm -rf internal/web/ui/dist && mkdir -p internal/web/ui/dist
COPY --from=ui /ui/dist/ internal/web/ui/dist/
# CGO off + static so the binary runs on distroless/static.
RUN CGO_ENABLED=0 go build -ldflags='-s -w' \
    -o /out/panel ./examples/20_server_panel

# ── Stage 3: minimal runtime ──────────────────────────────────────────────────
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=build /out/panel /panel
EXPOSE 9090
# Provide looper.json via a mount + LOOPER_CONFIG, and the LLM key via env:
#   docker run -p 9090:9090 -v "$PWD/looper.json:/looper.json:ro" \
#     -e LOOPER_CONFIG=/looper.json -e OPENAI_API_KEY looper-panel
ENTRYPOINT ["/panel"]
