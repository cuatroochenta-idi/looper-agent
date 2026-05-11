package web

import (
	"fmt"
	"net/http"
	"sync"
)

// SSEEvent represents a Server-Sent Event.
type SSEEvent struct {
	Event string
	Data  string
}

// SSEChannel is a single client's SSE connection.
type SSEChannel struct {
	ID       string // run ID this channel is subscribed to
	Events   chan SSEEvent
	done     chan struct{}
}

// SSEManager manages multiple SSE client connections, grouped by run ID.
type SSEManager struct {
	mu       sync.RWMutex
	channels map[string][]*SSEChannel // runID -> channels
}

// NewSSEManager creates a new SSE manager.
func NewSSEManager() *SSEManager {
	return &SSEManager{
		channels: make(map[string][]*SSEChannel),
	}
}

// Subscribe creates a new SSE channel for the given run ID.
func (m *SSEManager) Subscribe(runID string) *SSEChannel {
	ch := &SSEChannel{
		ID:     runID,
		Events: make(chan SSEEvent, 64),
		done:   make(chan struct{}),
	}
	m.mu.Lock()
	m.channels[runID] = append(m.channels[runID], ch)
	m.mu.Unlock()
	return ch
}

// Unsubscribe removes a channel.
func (m *SSEManager) Unsubscribe(runID string, ch *SSEChannel) {
	m.mu.Lock()
	defer m.mu.Unlock()
	channels := m.channels[runID]
	for i, c := range channels {
		if c == ch {
			m.channels[runID] = append(channels[:i], channels[i+1:]...)
			break
		}
	}
	close(ch.done)
}

// Send broadcasts an event to all channels for the given run ID.
func (m *SSEManager) Send(runID string, event SSEEvent) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, ch := range m.channels[runID] {
		select {
		case ch.Events <- event:
		default:
			// Drop if client is slow
		}
	}
}

// HandleStream is the HTTP handler for SSE connections.
func (m *SSEManager) HandleStream(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	if runID == "" {
		http.Error(w, "missing run ID", http.StatusBadRequest)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Send initial connection event
	fmt.Fprintf(w, "event: connected\ndata: <div class=\"live-entry\"><span class=\"ts\">--</span>Connected to run %s</div>\n\n", runID[:8])
	flusher.Flush()

	ch := m.Subscribe(runID)
	defer m.Unsubscribe(runID, ch)

	for {
		select {
		case event := <-ch.Events:
			if event.Event != "" {
				fmt.Fprintf(w, "event: %s\n", event.Event)
			}
			fmt.Fprintf(w, "data: %s\n\n", event.Data)
			flusher.Flush()
		case <-r.Context().Done():
			return
		case <-ch.done:
			return
		}
	}
}
