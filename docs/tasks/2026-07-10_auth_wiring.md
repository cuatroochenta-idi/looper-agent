# Auth + config wiring guide — 2026-07-10

How the coordinator wires `internal/config` and `internal/web/auth.go` into
`cmd/looper/serve.go` and `internal/web/server.go`. These two files were NOT
touched by the auth/config task (concurrent edit); everything below is the
integration the coordinator applies.

## Packages delivered

- `internal/config` — `config.Load(path)` → `config.Config` (file + env +
  defaults). See public API below.
- `internal/web/auth.go` — `web.NewAuth(...)` → `*web.Auth` with
  `Middleware`, `LoginHandler`, `LogoutHandler`, `MeHandler`.

## Precedence (already implemented in config.Load)

flags (caller) > env (`LOOPER_*`) > file (`looper.json`) > defaults
(Port 9090, StoreDir `.looper`). `Load` handles env+file+defaults; the serve
command applies flag overrides AFTER `Load` returns (only when the flag was
actually set — otherwise it would clobber file/env with defaults).

## serve.go wiring snippet

```go
// 1. Load config (path from --config flag; "" auto-discovers ./looper.json
//    then $LOOPER_CONFIG).
cfg, err := config.Load(*configPath)
if err != nil {
    return fmt.Errorf("load config: %w", err)
}

// 2. Apply flag overrides on top of config (flags win). Only overwrite when
//    the user actually passed the flag, so an unset flag keeps file/env value.
//    With stdlib `flag`, detect "was set" via flag.Visit, or compare against
//    a sentinel default. Example for --port:
flag.Visit(func(f *flag.Flag) {
    switch f.Name {
    case "port":
        cfg.Port = *portFlag
    case "store":
        cfg.StoreDir = *storeFlag
    case "db":
        cfg.DB = *dbFlag
    }
})

// 3. Build the web server as today (store dir / db come from cfg now).
srv := web.NewServer(/* existing args, using cfg.StoreDir / cfg.DB */)

// 4. Feed custom model costs into the telemetry cost model. cfg.ModelCosts
//    keys are "provider/model" or a bare model id; values are
//    telemetry.CostConfig. WithCustomCosts takes precedence over the built-in
//    matrix during estimation.
if len(cfg.ModelCosts) > 0 {
    costModel.WithCustomCosts(cfg.ModelCosts)
}
// NOTE: wherever the server/tracker constructs its *telemetry.CostModel,
// call WithCustomCosts on THAT instance before it prices any runs. If the
// web.Server owns the cost model internally, expose a setter or pass
// cfg.ModelCosts into web.NewServer so it can apply it — coordinator's call.

// 5. Build the auth layer. Nil-safe: when auth disabled, pass a nil *web.Auth
//    and Middleware becomes a pass-through.
var auth *web.Auth
if cfg.AuthEnabled() {
    a := cfg.Auth
    auth = web.NewAuth(a.Username, a.Password, a.SessionSecret, a.IngestToken).
        WithSecureCookies(serveOverTLS) // set true only when behind HTTPS

    // Print the ingest bearer token so agents/tracers can be configured.
    // (Always prints the effective token, generated or configured.)
    log.Printf("panel auth enabled; ingest bearer token: %s", auth.IngestToken())
    if auth.EphemeralSessionKey() {
        log.Printf("warning: no auth.session_secret set — sessions will not " +
            "survive a restart (set session_secret in looper.json to persist)")
    }
}

// 6. Wrap the server handler with auth middleware, then mount the three auth
//    endpoints OUTSIDE the wrapped handler's guard is NOT needed — Middleware
//    already whitelists POST /api/login, POST /api/logout, GET /api/me. So
//    the simplest correct wiring is: register the three handlers on the SAME
//    mux the server builds, then wrap the whole mux.
//
//    Since server.Handler() returns the fully-built mux, mount the auth routes
//    by composing a parent mux:
root := http.NewServeMux()
if auth != nil {
    root.HandleFunc("POST /api/login", auth.LoginHandler)
    root.HandleFunc("POST /api/logout", auth.LogoutHandler)
    root.HandleFunc("GET /api/me", auth.MeHandler)
}
root.Handle("/", srv.Handler()) // everything else falls through to the server
handler := auth.Middleware(root) // nil auth => pass-through

httpSrv := &http.Server{
    Addr:    fmt.Sprintf(":%d", cfg.Port),
    Handler: handler,
}
```

### Route-precedence note (Go 1.22+ mux)

`http.ServeMux` matches the most specific pattern. `root.Handle("/", ...)` is a
catch-all; the explicit `POST /api/login` / `POST /api/logout` /
`GET /api/me` patterns on `root` win over it, so the auth handlers shadow any
same-path handler inside `srv.Handler()`. If the server ever registers its own
`/api/me` etc., these auth routes take precedence — intended.

If auth is disabled (`auth == nil`), skip registering the three routes (or
register them anyway — `LoginHandler`/`MeHandler`/`LogoutHandler` are nil-safe
and behave as an open panel). `Middleware(root)` on a nil `*Auth` returns
`root` unwrapped.

## Ingest / tracer side (coordinator implements — OUT OF THIS TASK's SCOPE)

`looper/tracer.go` POSTs TraceEvents to `LOOPER_TRACE_ENDPOINT`. When auth is
on, `/ingest` requires `Authorization: Bearer <ingest_token>`. The tracer must
attach that header when `LOOPER_INGEST_TOKEN` is set in its environment:

```go
// in the tracer's POST builder:
if tok := os.Getenv("LOOPER_INGEST_TOKEN"); tok != "" {
    req.Header.Set("Authorization", "Bearer "+tok)
}
```

Examples (`examples/20_server_panel`, etc.) set `LOOPER_INGEST_TOKEN` in the
child agent's environment to the value the server printed at boot. The server's
`config.Load` also reads `LOOPER_INGEST_TOKEN` (via `AuthConfig.IngestToken`),
so a single env var can pin both sides to the same token in local dev — set it
once and neither side auto-generates.

## Auth semantics reference (for the SPA / coordinator)

- Session cookie `looper_session` = `v1:<expiry-unix>:<hmac-sha256(secret,expiry)>`,
  HttpOnly, SameSite=Lax, Path=/, 7-day expiry, constant-time verified. Secure
  flag set only via `WithSecureCookies(true)`.
- `POST /api/login {username?, password}` → 204 + cookie | 401 | 429 (rate
  limited: 5 attempts/min per IP, in-memory fixed window).
- `POST /api/logout` → 204 + expired cookie.
- `GET /api/me` → `{auth_enabled, authenticated, username?}`. Works with auth
  disabled (nil Auth) reporting `auth_enabled:false, authenticated:true`.
- Middleware: whitelists login/logout/me; `/ingest` needs bearer; everything
  else needs a valid cookie. 401 JSON `{"error":"unauthorized"}` for `/api/*`,
  bare 401 otherwise. NO server-side redirects — the SPA routes to login.
- No password hashing by design: config stores the password in plaintext, so
  hashing at the HTTP layer protects nothing the config-reader lacks.

## config public API surface

```go
type Config struct {
    Port       int
    DB         string
    StoreDir   string
    Auth       *AuthConfig
    ModelCosts map[string]telemetry.CostConfig
}
func (c Config) AuthEnabled() bool
func Load(path string) (Config, error)

type AuthConfig struct {
    Username, Password, SessionSecret, IngestToken string
}

const (
    DefaultPort     = 9090
    DefaultStoreDir = ".looper"
)
```

Env vars honored by `Load`: `LOOPER_PORT`, `LOOPER_DB`, `LOOPER_STORE_DIR`,
`LOOPER_AUTH_USERNAME`, `LOOPER_AUTH_PASSWORD`, `LOOPER_SESSION_SECRET`,
`LOOPER_INGEST_TOKEN`, plus `LOOPER_CONFIG` (config file path when `--config`
unset). An unknown field in `looper.json` is a hard error (DisallowUnknownFields).

## web.Auth public API surface

```go
func NewAuth(username, password, sessionSecret, ingestToken string) *Auth
func (a *Auth) WithSecureCookies(secure bool) *Auth
func (a *Auth) IngestToken() string            // effective token (generated or configured)
func (a *Auth) EphemeralSessionKey() bool      // true when no stable secret set
func (a *Auth) Middleware(next http.Handler) http.Handler
func (a *Auth) LoginHandler(w http.ResponseWriter, r *http.Request)
func (a *Auth) LogoutHandler(w http.ResponseWriter, r *http.Request)
func (a *Auth) MeHandler(w http.ResponseWriter, r *http.Request)
```

All methods are nil-safe: a nil `*Auth` is "auth disabled".
