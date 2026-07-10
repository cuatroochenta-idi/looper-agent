package web

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/cuatroochenta-idi/looper-agent/internal/web/ui"
)

// stuckRunMaxIdle is the cap on how long a run may stay "running" with no
// new step events before the sweeper finalizes it as "unknown". Picked to be
// comfortably longer than any reasonable LLM turn (incl. slow tool calls and
// long sub-agent fan-outs) so legitimate work isn't discarded, while still
// clearing a dead process out of the "running" filter within minutes.
const stuckRunMaxIdle = 10 * time.Minute

// stuckRunSweepInterval is how often the background sweeper runs.
const stuckRunSweepInterval = 30 * time.Second

// Server hosts the debug panel. Construct with NewServer, attach a RunFunc
// with SetRunner, then mount Handler() on any HTTP listener.
type Server struct {
	store    *Store
	hub      *Hub
	runner   RunFunc
	persist  Persistence // durable backend; nil = in-memory only
	basePath string      // public mount path of the SPA; "" or "/" = root
}

// Hub exposes the pub/sub bus so callers can publish or subscribe explicitly.
func (s *Server) Hub() *Hub { return s.hub }

// ServerOption configures a Server at construction time.
type ServerOption func(*Server)

// WithPersistence sets the durable backend for finalized runs. A nil argument
// keeps the panel in-memory only. See internal/store/postgres for the SQL
// backend; folderPersistence (via WithStoreDir) is the default.
func WithPersistence(p Persistence) ServerOption {
	return func(s *Server) { s.persist = p }
}

// WithStoreDir sets the directory used to persist completed runs as JSON —
// sugar for WithPersistence with a folder backend. On startup, existing files
// in that directory are loaded into the store. An empty string disables
// persistence (in-memory only).
func WithStoreDir(dir string) ServerOption {
	return func(s *Server) {
		if dir == "" {
			s.persist = nil
			return
		}
		p, err := NewFolderPersistence(dir)
		if err != nil {
			log.Printf("warn: store dir setup: %v", err)
			return
		}
		s.persist = p
	}
}

// WithBasePath declares the public path the panel is mounted under (e.g.
// "/admin/looper/"). serveIndex injects it as window.__LOOPER_BASE__ plus a
// <base> tag, and the SPA builds every asset URL, API call, SSE stream, and
// client route from it. Hosts mount Handler() behind an http.StripPrefix of
// the same value. Empty (or "/") means served at root.
func WithBasePath(base string) ServerOption {
	return func(s *Server) { s.basePath = base }
}

// NewServer returns a server ready to serve HTTP. If a store directory is
// configured, it is created (if missing), gitignored, and any pre-existing
// runs are hydrated into the in-memory store. A background sweeper is also
// started that finalizes runs stuck in "running" past stuckRunMaxIdle so the
// panel never displays a permanently "thinking…" bubble when an agent
// process dies before emitting run_end.
func NewServer(opts ...ServerOption) (*Server, error) {
	s := &Server{store: NewStore(), hub: NewHub()}
	for _, opt := range opts {
		opt(s)
	}
	if s.persist != nil {
		runs, err := s.persist.LoadRuns()
		if err != nil {
			log.Printf("warn: failed to hydrate runs: %v", err)
		} else {
			for _, r := range runs {
				s.store.Add(r)
			}
			if len(runs) > 0 {
				log.Printf("Loaded %d runs", len(runs))
			}
		}
		// Any run that came back from storage in "running" state belongs to a
		// previous process that's now gone — finalize immediately instead of
		// waiting for the first sweep tick.
		_ = s.store.SweepStuckRuns(0, time.Now())
	}
	go s.runStuckRunSweeper()
	return s, nil
}

// runStuckRunSweeper periodically calls SweepStuckRuns and publishes typed
// events for any run that was finalized so the SPA updates in real time.
func (s *Server) runStuckRunSweeper() {
	ticker := time.NewTicker(stuckRunSweepInterval)
	defer ticker.Stop()
	for range ticker.C {
		finalized := s.store.SweepStuckRuns(stuckRunMaxIdle, time.Now())
		if len(finalized) == 0 {
			continue
		}
		// Publish first — waking the UI must not wait on disk I/O.
		for _, id := range finalized {
			s.publishRunUpdated(id, TopicRun(id), TopicRuns, TopicSummary)
		}
		s.publishRunsChanged()
		s.publishChatsChanged()
		if s.persist != nil {
			for _, id := range finalized {
				if r := s.store.Find(id); r != nil {
					_ = s.persist.SaveRun(r)
				}
			}
		}
	}
}

// SetRunner wires the agent runner used by POST /api/run. If unset, the
// endpoint returns an error instead of executing anything.
func (s *Server) SetRunner(fn RunFunc) { s.runner = fn }

// Store exposes the underlying run store (used by tests and integrations).
func (s *Server) Store() *Store { return s.store }

// Handler returns the http.Handler with every route registered.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// ── JSON REST ──────────────────────────────────────────────────────────────
	mux.HandleFunc("GET /api/state/summary", s.apiSummary)
	mux.HandleFunc("GET /api/state/runs", s.apiRuns)
	mux.HandleFunc("GET /api/state/runs/{id}", s.apiRunDetail)
	mux.HandleFunc("GET /api/state/chats", s.apiChats)
	mux.HandleFunc("GET /api/state/chats/{key}", s.apiChatDetail)
	mux.HandleFunc("GET /api/state/costs", s.apiCosts)
	mux.HandleFunc("POST /api/run", s.apiRun)

	// ── Typed JSON SSE ─────────────────────────────────────────────────────────
	mux.HandleFunc("GET /api/events", s.handleEvents)

	// ── Trace ingestion (external agents). Wire format unchanged. ──────────────
	mux.HandleFunc("POST /ingest", s.apiIngest)

	// ── SPA (client-side routing; index.html fallback) ─────────────────────────
	mux.Handle("GET /", s.spaHandler())

	return mux
}

// spaHandler serves the embedded SPA bundle. Hashed assets get an immutable
// cache header; any GET path that isn't a real file falls back to index.html so
// the client-side router can handle it. /api and /ingest are matched by more
// specific patterns and never reach here.
func (s *Server) spaHandler() http.Handler {
	dist, err := fs.Sub(ui.Dist, "dist")
	if err != nil {
		// The embed always contains dist/; a failure here is a build-time bug.
		log.Printf("warn: SPA embed unavailable: %v", err)
		return http.NotFoundHandler()
	}
	// The index page is templated once: the configured base path is injected
	// so the SPA (and its relative asset URLs, via <base>) work both at root
	// and mounted under a host subpath.
	index, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		log.Printf("warn: SPA index.html missing from embed: %v", err)
		return http.NotFoundHandler()
	}
	page := injectBasePath(index, s.basePath)

	fileServer := http.FileServer(http.FS(dist))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			serveIndex(w, page)
			return
		}
		f, err := dist.Open(p)
		if err != nil {
			serveIndex(w, page) // unknown path → client-side route
			return
		}
		_ = f.Close()
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		fileServer.ServeHTTP(w, r)
	})
}

// injectBasePath rewrites the SPA index page for a mount point: a <base> tag
// resolves the bundle's relative asset URLs on deep client routes, and
// window.__LOOPER_BASE__ tells the SPA where to aim API calls, SSE streams,
// and its router. base is normalized to "/segment/.../" form; empty → "/".
func injectBasePath(index []byte, base string) []byte {
	b := "/" + strings.Trim(base, "/")
	if b != "/" {
		b += "/"
	}
	quoted, _ := json.Marshal(b) // safe JS string literal
	tags := fmt.Sprintf("<base href=%s><script>window.__LOOPER_BASE__=%s;</script>", quoted, quoted)
	if i := strings.Index(string(index), "<head>"); i >= 0 {
		at := i + len("<head>")
		out := make([]byte, 0, len(index)+len(tags))
		out = append(out, index[:at]...)
		out = append(out, tags...)
		out = append(out, index[at:]...)
		return out
	}
	return index
}

func serveIndex(w http.ResponseWriter, page []byte) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(page)
}
