// Package web implements the Looper Agent web UI using html/template + htmx + SSE.
// It provides a dashboard, run history, live run streaming, and API endpoints
// for developer observability and debugging.
package web

import (
	"embed"
	"html/template"
	"io/fs"
	"net/http"
	"sync"
	"time"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

// RunRecord stores metadata about a completed or in-progress run.
type RunRecord struct {
	ID        string    `json:"id"`
	Input     string    `json:"input"`
	Output    string    `json:"output"`
	Status    string    `json:"status"` // running, completed, error, cancelled
	Turns     int       `json:"turns"`
	TotalUSD  float64   `json:"total_usd"`
	Tokens    int       `json:"tokens"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
}

// Store is an in-memory store for run records.
type Store struct {
	mu   sync.RWMutex
	runs []*RunRecord
}

func NewStore() *Store {
	return &Store{runs: make([]*RunRecord, 0)}
}

func (s *Store) Add(r *RunRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs = append(s.runs, r)
}

func (s *Store) All() []*RunRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cp := make([]*RunRecord, len(s.runs))
	copy(cp, s.runs)
	return cp
}

// Server is the HTTP server for the Looper web UI.
type Server struct {
	store     *Store
	templates *template.Template
	sse       *SSEManager
}

// NewServer creates a new web UI server.
func NewServer() (*Server, error) {
	tmpl, err := template.ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return nil, err
	}

	return &Server{
		store:     NewStore(),
		templates: tmpl,
		sse:       NewSSEManager(),
	}, nil
}

// Handler returns the HTTP handler for the web UI.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Pages
	mux.HandleFunc("GET /", s.handleDashboard)
	mux.HandleFunc("GET /runs", s.handleRuns)
	mux.HandleFunc("GET /runs/{id}", s.handleRunDetail)
	mux.HandleFunc("GET /live", s.handleLive)
	mux.HandleFunc("GET /live/{id}", s.handleLiveRun)

	// API
	mux.HandleFunc("POST /api/run", s.handleAPIRun)
	mux.HandleFunc("GET /api/runs", s.handleAPIRuns)
	mux.HandleFunc("GET /api/costs", s.handleAPICosts)

	// SSE
	mux.HandleFunc("GET /api/stream/{id}", s.sse.HandleStream)

	// Static
	staticSub, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))

	return mux
}

// Store returns the run store.
func (s *Server) Store() *Store { return s.store }

// SSEManager returns the SSE manager.
func (s *Server) SSEManager() *SSEManager { return s.sse }
