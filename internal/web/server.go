package web

import (
	"log"
	"net/http"
	"time"
)

// stuckRunMaxIdle is the cap on how long a run may stay "running" with no
// new step events before the sweeper auto-finalizes it as errored. Picked
// to be longer than any reasonable LLM turn so legitimate slow calls
// aren't killed, but short enough that a dead process clears within
// minutes instead of hanging the panel forever.
const stuckRunMaxIdle = 3 * time.Minute

// stuckRunSweepInterval is how often the background sweeper runs.
const stuckRunSweepInterval = 30 * time.Second

// Server hosts the debug panel. Construct with NewServer, attach a RunFunc
// with SetRunner, then mount Handler() on any HTTP listener.
type Server struct {
	store    *Store
	hub      *Hub
	runner   RunFunc
	storeDir string // on-disk persistence root, e.g. ".looper"
}

// Hub exposes the pub/sub bus so callers can publish or subscribe explicitly.
func (s *Server) Hub() *Hub { return s.hub }

// ServerOption configures a Server at construction time.
type ServerOption func(*Server)

// WithStoreDir sets the directory used to persist completed runs as JSON.
// On startup, existing files in that directory are loaded into the store.
// An empty string disables persistence.
func WithStoreDir(dir string) ServerOption {
	return func(s *Server) { s.storeDir = dir }
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
	if s.storeDir != "" {
		if err := ensureStoreDir(s.storeDir); err != nil {
			log.Printf("warn: store dir setup: %v", err)
		}
		n, err := loadRunsFromDisk(s.storeDir, s.store)
		if err != nil {
			log.Printf("warn: failed to hydrate runs from %s: %v", s.storeDir, err)
		} else if n > 0 {
			log.Printf("Loaded %d runs from %s", n, s.storeDir)
		}
		// Any run that came back from disk in "running" state belongs to a
		// previous process that's now gone — finalize immediately instead of
		// waiting for the first sweep tick.
		_ = s.store.SweepStuckRuns(0, time.Now())
	}
	go s.runStuckRunSweeper()
	return s, nil
}

// runStuckRunSweeper periodically calls SweepStuckRuns and publishes hub
// notifications for any run that was finalized so the sidebar + detail
// pane update in real time.
func (s *Server) runStuckRunSweeper() {
	ticker := time.NewTicker(stuckRunSweepInterval)
	defer ticker.Stop()
	for range ticker.C {
		finalized := s.store.SweepStuckRuns(stuckRunMaxIdle, time.Now())
		if len(finalized) == 0 {
			continue
		}
		topics := make([]Topic, 0, len(finalized)+2)
		topics = append(topics, TopicSidebar, TopicChats)
		for _, id := range finalized {
			topics = append(topics, TopicRun(id))
			if s.storeDir != "" {
				if r := s.store.Find(id); r != nil {
					_ = writeRunFile(s.storeDir, r)
				}
			}
		}
		s.hub.Publish(topics...)
	}
}

// SetRunner wires the agent runner used by POST /api/run. If unset, the
// endpoint returns an error event instead of executing anything.
func (s *Server) SetRunner(fn RunFunc) { s.runner = fn }

// Store exposes the underlying run store (used by tests and integrations).
func (s *Server) Store() *Store { return s.store }

// Handler returns the http.Handler with every route registered.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// ── Pages ────────────────────────────────────────────────────────────────
	// {$} pins the dashboard to the literal root path (Go 1.22+ semantics) so
	// any unmatched URL falls through to the 404 below, instead of silently
	// serving the dashboard from a stale URL.
	mux.HandleFunc("GET /{$}", s.handleDashboard)
	mux.HandleFunc("GET /runs", s.handleRunsPage)
	mux.HandleFunc("GET /runs/{id}", s.handleRunPage)
	mux.HandleFunc("GET /chats", s.handleChatsPage)

	// ── HTML fragments (datastar patches them into the DOM) ─────────────────
	// One-shot fragments for cold loads / fallback polls.
	mux.HandleFunc("GET /partials/sidebar", s.partialSidebar)
	mux.HandleFunc("GET /partials/runs/{id}/pane", s.partialDetailPane)
	mux.HandleFunc("GET /partials/dashboard", s.partialDashboard)
	mux.HandleFunc("GET /partials/chat-sidebar", s.partialChatSidebar)
	mux.HandleFunc("GET /partials/chat-thread", s.partialChatThread)
	mux.HandleFunc("GET /partials/chat-trace/{id}", s.partialChatTrace)
	mux.HandleFunc("GET /partials/time-refresh/{since}", s.partialTimeRefresh)
	// Long-lived SSE streams: server pushes a fresh fragment to every
	// connected client when the underlying state changes. This replaces
	// polling for real-time UX.
	mux.HandleFunc("GET /sse/sidebar", s.sseSidebar)
	mux.HandleFunc("GET /sse/runs/{id}", s.sseDetailPane)
	mux.HandleFunc("GET /sse/dashboard", s.sseDashboard)
	mux.HandleFunc("GET /sse/chat-sidebar", s.sseChatSidebar)
	mux.HandleFunc("GET /sse/chat-thread", s.sseChatThread)
	mux.HandleFunc("GET /sse/chat-trace/{id}", s.sseChatTrace)

	// ── JSON / control ───────────────────────────────────────────────────────
	mux.HandleFunc("POST /api/run", s.apiRun)
	mux.HandleFunc("GET /api/runs", s.apiListRuns)
	mux.HandleFunc("GET /api/costs", s.apiCosts)
	// Trace ingestion endpoint for external agents. Receives one event per
	// POST (run_start / step / run_end). See agent.go in the root package.
	mux.HandleFunc("POST /ingest", s.apiIngest)

	return mux
}
