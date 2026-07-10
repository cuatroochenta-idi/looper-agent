# 20 · Server panel — deploy the core as a supervision server

The other examples show the framework. This one shows the **production
deployment pattern**: instead of running the shipped `looper serve` CLI, you
embed `internal/web.Server` in your own binary and run it as a long-lived
**control panel** for your agents.

## What it demonstrates

- **Your own control panel + chat supervision.** The panel is a first-class part
  of your service, not a side-car. Ops logs in and watches every run — prompts,
  tool calls, outputs, live token/cost rollups — and can kick off runs from the
  chat surface.
- **A login gate (auth).** The panel exposes everything an agent said and spent,
  so in prod it must sit behind auth. Setting `auth.password` turns on an HMAC
  session cookie login; the middleware guards every route.
- **An ingest token for external agents.** Other processes/services point their
  tracer at this panel's `/ingest` and their runs appear here too. When auth is
  on, `/ingest` requires a bearer token — the server logs the effective token at
  boot so you can configure those agents.
- **A custom pricing dictionary.** `looper.json`'s `model_costs` overrides the
  built-in price matrix, so cost tracking reflects *your* negotiated / gateway
  rates.
- **Folder vs Postgres persistence.** Folder store by default (zero-dependency,
  great for a single box); set `db` to a PostgreSQL DSN for a durable,
  multi-replica prod backend (Atlas-authored migrations live under
  `internal/store/postgres/migrations`).
- **A sub-agent-spawning tool.** The in-process "support" agent's `escalate`
  tool spins up a specialist sub-agent inside the tool body and forwards `ctx`,
  so the panel renders the parent→child run tree (`ParentRunID` linkage).

## Run it

```bash
# 1. Build the REAL SolidJS UI into the binary (otherwise you get the
#    placeholder page). Bun is required for this step only.
make ui-build

# 2. Configure: copy the sample and edit the password.
cp examples/20_server_panel/looper.example.json looper.json

# 3. Provide an LLM key and (optionally) pick a provider.
export OPENAI_API_KEY=sk-...          # or ANTHROPIC_API_KEY / GOOGLE_API_KEY
export LOOPER_PROVIDER=openai         # openai (default) | anthropic | google

# 4. Run.
go run ./examples/20_server_panel
# → open http://localhost:9090 and log in with the credentials from looper.json
```

`config.Load` auto-discovers `./looper.json`. Precedence is
**flags > env (`LOOPER_*`) > file > defaults**, so you can override any field
without editing the file (e.g. `LOOPER_PORT=8080`, `LOOPER_DB=postgres://…`,
`LOOPER_AUTH_PASSWORD=…`).

## How external agents connect

Point any other Looper-based process at this panel by setting, in *its*
environment:

```bash
export LOOPER_TRACE_ENDPOINT=http://<host>:9090/ingest
export LOOPER_INGEST_TOKEN=<the bearer token this server logged at boot>
export LOOPER_SESSION_ID="$(uuidgen)"   # groups its runs into one conversation
go run ./your-agent
```

Its runs stream into this panel live, grouped by session, priced with your
custom `model_costs`.

## Switching to Postgres

```bash
# looper.json:  "db": "postgres://user:pass@host:5432/looper?sslmode=disable"
# or:
export LOOPER_DB="postgres://user:pass@host:5432/looper?sslmode=disable"
```

`db` overrides the folder store. Apply the versioned migrations first (see the
repo-root `make db-diff` / `internal/store/postgres/migrations`).

## Illustrative Dockerfile

`Dockerfile` in this folder is a **reference** multi-stage build (Bun stage
builds the UI → Go stage compiles the binary with the embedded bundle →
distroless/static final). It is not wired into CI — treat it as a starting
point. Build it from the repo root:

```bash
docker build -f examples/20_server_panel/Dockerfile -t looper-panel .
docker run --rm -p 9090:9090 -v "$PWD/looper.json:/looper.json:ro" \
  -e OPENAI_API_KEY -e LOOPER_CONFIG=/looper.json looper-panel
```
