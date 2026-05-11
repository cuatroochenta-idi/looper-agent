// Package web implements the Looper Agent web UI using html/template + htmx + SSE.
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

type RunRecord struct {
	ID        string    `json:"id"`
	Input     string    `json:"input"`
	Output    string    `json:"output"`
	Status    string    `json:"status"`
	Turns     int       `json:"turns"`
	TotalUSD  float64   `json:"total_usd"`
	Tokens    int       `json:"tokens"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
}

type Store struct {
	mu   sync.RWMutex
	runs []*RunRecord
}

func NewStore() *Store { return &Store{runs: make([]*RunRecord, 0)} }

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

type Server struct {
	store     *Store
	templates *template.Template
	sse       *SSEManager
}

func NewServer() (*Server, error) {
	tmpl, err := template.ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &Server{store: NewStore(), templates: tmpl, sse: NewSSEManager()}, nil
}

func isHX(r *http.Request) bool { return r.Header.Get("HX-Request") == "true" }

// renderFull renders the full page (base.html + content block).
func (s *Server) renderFull(w http.ResponseWriter, title, contentTmpl string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	data["Title"] = title
	data["ContentTemplate"] = contentTmpl
	s.templates.ExecuteTemplate(w, "base.html", data)
}

// renderHX renders just the content fragment for htmx swap.
func (s *Server) renderHX(w http.ResponseWriter, contentTmpl string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	s.templates.ExecuteTemplate(w, contentTmpl, data)
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /", s.handleDashboard)
	mux.HandleFunc("GET /runs", s.handleRuns)
	mux.HandleFunc("GET /runs/{id}", s.handleRunDetail)
	mux.HandleFunc("GET /live", s.handleLive)
	mux.HandleFunc("GET /live/{id}", s.handleLiveRun)

	mux.HandleFunc("POST /api/run", s.handleAPIRun)
	mux.HandleFunc("GET /api/runs", s.handleAPIRuns)
	mux.HandleFunc("GET /api/costs", s.handleAPICosts)

	mux.HandleFunc("GET /api/stream/{id}", s.sse.HandleStream)

	staticSub, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))

	return mux
}

func (s *Server) Store() *Store       { return s.store }
func (s *Server) SSEManager() *SSEManager { return s.sse }
