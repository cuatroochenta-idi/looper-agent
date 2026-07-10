// Package analytics embeds the looper supervision panel — SPA, JSON REST API,
// typed SSE stream, trace ingest, and durable run store — inside a host Go
// program, so observability ships in the same binary instead of a sidecar
// `looper serve` process.
//
// The package is a thin composition of the CLI's own building blocks
// (internal/web server, internal/store backends, the looper trace pipeline)
// exposed through three ports:
//
//   - Store connectors: Config.PostgresDSN (versioned migrations applied on
//     boot) or Config.StoreDir (one JSON file per run); neither = in-memory.
//   - HTTP: Handler() is a plain http.Handler the host mounts wherever it
//     wants — typically behind its own authentication / permission
//     middleware, which is exactly why the embedded panel ships without a
//     login gate of its own.
//   - Tracing: Panel implements looper.TraceSink, so agents built in the
//     same process report runs directly into the panel's store with
//     looper.WithTraceSink(panel) — no HTTP hop, no LOOPER_TRACE_ENDPOINT.
//
// Mounting under a subpath (host router owns the prefix):
//
//	panel, err := analytics.New(ctx, analytics.Config{
//		PostgresDSN: os.Getenv("LOOPER_DB"),
//		BasePath:    "/admin/looper/",
//	})
//	mux.Handle("/admin/looper/", requirePermission("observability",
//		http.StripPrefix("/admin/looper", panel.Handler())))
//
//	agent, err := looper.NewAgent(prov, prompt, looper.WithTraceSink(panel))
//
// External agents (other processes) can still POST to the mounted /ingest
// route; set Config.IngestToken to require a bearer token on that route
// when the host middleware does not already cover machine-to-machine calls.
package analytics

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"

	"github.com/cuatroochenta-idi/looper-agent/internal/store/postgres"
	"github.com/cuatroochenta-idi/looper-agent/internal/web"
	"github.com/cuatroochenta-idi/looper-agent/looper"
)

// Config selects the panel's store connector and mount contract.
type Config struct {
	// PostgresDSN selects the PostgreSQL store (embedded migrations are
	// applied on boot). Wins over StoreDir. Example:
	// "postgres://looper:…@db:5432/app?sslmode=disable&search_path=looper".
	PostgresDSN string

	// StoreDir selects the folder store (one JSON file per finalized run).
	// Used only when PostgresDSN is empty. Empty too = in-memory only.
	StoreDir string

	// BasePath is the public path the host mounts Handler() under (e.g.
	// "/admin/looper/"). It is injected into the SPA so assets, API calls,
	// SSE, and client routes resolve correctly. Empty means served at root.
	// The host must strip the same prefix (http.StripPrefix) before
	// delegating to Handler().
	BasePath string

	// IngestToken, when set, requires "Authorization: Bearer <token>" on
	// POST /ingest — for external agent processes reporting over HTTP.
	// In-process tracing via looper.WithTraceSink never touches /ingest
	// and needs no token. Leave empty when the host middleware already
	// authenticates every route.
	IngestToken string
}

// Panel is an embedded supervision panel. It is an http.Handler factory
// (Handler) and a looper.TraceSink (TraceEvent), so one value wires both the
// serving side and the reporting side of in-process observability.
type Panel struct {
	srv     *web.Server
	handler http.Handler
	persist web.Persistence // nil when in-memory only
}

// Panel receives agent trace events in-process.
var _ looper.TraceSink = (*Panel)(nil)

// New builds a Panel from cfg: opens the selected store connector, hydrates
// previously persisted runs, and prepares the HTTP surface. Call Close on
// shutdown to release the store.
func New(ctx context.Context, cfg Config) (*Panel, error) {
	var persist web.Persistence
	switch {
	case cfg.PostgresDSN != "":
		pg, err := postgres.NewPostgres(ctx, cfg.PostgresDSN)
		if err != nil {
			return nil, fmt.Errorf("analytics: postgres store: %w", err)
		}
		persist = pg
	case cfg.StoreDir != "":
		fp, err := web.NewFolderPersistence(cfg.StoreDir)
		if err != nil {
			return nil, fmt.Errorf("analytics: folder store: %w", err)
		}
		persist = fp
	}

	srv, err := web.NewServer(
		web.WithPersistence(persist),
		web.WithBasePath(cfg.BasePath),
	)
	if err != nil {
		if persist != nil {
			_ = persist.Close()
		}
		return nil, fmt.Errorf("analytics: server: %w", err)
	}

	p := &Panel{srv: srv, persist: persist}
	p.handler = buildHandler(srv, cfg.IngestToken)
	return p, nil
}

// Handler returns the panel's full HTTP surface: SPA, /api/state/*,
// /api/events (SSE), /ingest, and the /api/me probe the SPA uses to skip its
// login screen (authentication belongs to the host's middleware). Mount it
// behind http.StripPrefix when BasePath is set.
func (p *Panel) Handler() http.Handler { return p.handler }

// TraceEvent implements looper.TraceSink: events from agents in the same
// process are applied straight to the panel store — no HTTP hop. Malformed
// events are dropped, matching the HTTP transport's fire-and-forget contract
// (observability must never break the host program).
func (p *Panel) TraceEvent(ev looper.TraceEvent) {
	_ = p.srv.IngestEvent(web.TraceEvent{
		Type:             string(ev.Type),
		RunID:            ev.RunID,
		ParentRunID:      ev.ParentRunID,
		ParentToolCallID: ev.ParentToolCallID,
		SessionID:        ev.SessionID,
		Ts:               ev.Ts,
		Project:          ev.Project,
		Data:             ev.Data,
	})
}

// Close releases the store connector. The Panel must not be used afterwards.
func (p *Panel) Close() error {
	if p.persist != nil {
		return p.persist.Close()
	}
	return nil
}

// buildHandler assembles the embedded HTTP surface. The auth trio mounts with
// a nil *web.Auth (every handler is nil-receiver-safe and reports an open
// panel) because authentication is the host middleware's job — but the SPA
// still probes GET /api/me and must receive JSON, not the SPA fallback.
func buildHandler(srv *web.Server, ingestToken string) http.Handler {
	var auth *web.Auth
	root := http.NewServeMux()
	root.HandleFunc("POST /api/login", auth.LoginHandler)
	root.HandleFunc("POST /api/logout", auth.LogoutHandler)
	root.HandleFunc("GET /api/me", auth.MeHandler)
	root.Handle("/", srv.Handler())
	if ingestToken == "" {
		return root
	}
	return requireIngestBearer(ingestToken, root)
}

// requireIngestBearer guards POST /ingest with a constant-time bearer check,
// leaving every other route untouched (those are the host middleware's turf).
func requireIngestBearer(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSuffix(r.URL.Path, "/") == "/ingest" {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
