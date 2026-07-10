package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"
)

// events.go implements the typed JSON SSE stream documented in the API
// contract. One multiplexed connection per client: GET /api/events?topics=a,b,c
// where topics ∈ {runs, chats, summary, run:<id>}. Events carry small JSON
// deltas; a client that misses events just refetches a REST snapshot, so every
// event is safe to drop.

const (
	// sseWriteTimeout bounds each individual SSE write. The http.Server runs
	// without a global WriteTimeout (it would kill long-lived streams), so a
	// half-dead client is caught by this per-write deadline instead of pinning
	// the goroutine once the kernel buffer fills.
	sseWriteTimeout = 15 * time.Second
	// sseHeartbeat is the comment keep-alive interval; middleboxes drop idle
	// connections otherwise.
	sseHeartbeat = 25 * time.Second
)

// ─── Event payloads ───────────────────────────────────────────────────────────

// runUpdatedPayload is the small delta for list rows + summary tiles.
type runUpdatedPayload struct {
	ID          string    `json:"id"`
	ParentRunID string    `json:"parent_run_id,omitempty"`
	Kind        string    `json:"kind"`
	Status      string    `json:"status"`
	LastSeenAt  time.Time `json:"last_seen_at"`
	SelfUSD     float64   `json:"self_usd"`
	SubtreeUSD  float64   `json:"subtree_usd"`
	Turns       int       `json:"turns"`
	Tokens      int       `json:"tokens"`
}

// stepPayloadView is the persisted step carried by step_appended.
type stepPayloadView struct {
	Kind       string     `json:"kind"`
	Turn       int        `json:"turn"`
	Content    string     `json:"content,omitempty"`
	ToolName   string     `json:"tool_name,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Err        string     `json:"err,omitempty"`
	At         time.Time  `json:"at"`
	Usage      *UsageView `json:"usage,omitempty"`
	Provider   string     `json:"provider,omitempty"`
	Model      string     `json:"model,omitempty"`
}

type stepAppendedPayload struct {
	RunID string          `json:"run_id"`
	Step  stepPayloadView `json:"step"`
}

// chunkPayload is a LIVE-ONLY streaming delta. Never persisted; emitted only to
// run:<id> subscribers.
type chunkPayload struct {
	RunID string `json:"run_id"`
	Turn  int    `json:"turn"`
	Kind  string `json:"kind"` // "text" | "reasoning"
	Delta string `json:"delta"`
}

// ─── Publish helpers ──────────────────────────────────────────────────────────

func (s *Server) publishRunsChanged() {
	s.hub.Publish(Event{Name: "runs_changed", Data: struct{}{}}, TopicRuns)
}

func (s *Server) publishChatsChanged() {
	s.hub.Publish(Event{Name: "chats_changed", Data: struct{}{}}, TopicChats)
}

// publishRunUpdated emits a run_updated delta to the given topics. The self /
// subtree costs are recomputed from the full store so the row reflects any
// subagent that contributed since the last event.
func (s *Server) publishRunUpdated(id string, topics ...Topic) {
	run := s.store.Find(id)
	if run == nil {
		return
	}
	all := s.store.All()
	rollup := buildRollups(all, childrenByParent(all))[id]
	s.hub.Publish(Event{Name: "run_updated", Data: runUpdatedPayload{
		ID:          run.ID,
		ParentRunID: run.ParentRunID,
		Kind:        runKind(run),
		Status:      string(run.Status),
		LastSeenAt:  effLastSeen(run),
		SelfUSD:     round8(rollup.SelfUSD),
		SubtreeUSD:  round8(rollup.TotalUSD()),
		Turns:       run.Turns,
		Tokens:      run.Tokens,
	}}, topics...)
}

func (s *Server) publishStepAppended(id string, step TimelineStep) {
	sv := stepPayloadView{
		Kind:       string(step.Kind),
		Turn:       step.Turn,
		Content:    step.Content,
		ToolName:   step.ToolName,
		ToolCallID: step.ToolCallID,
		Err:        step.Err,
		At:         step.At,
		Provider:   step.Provider,
		Model:      step.Model,
	}
	if step.InputTokens > 0 || step.OutputTokens > 0 {
		sv.Usage = &UsageView{InputTokens: step.InputTokens, OutputTokens: step.OutputTokens, CachedTokens: step.CachedTokens}
	}
	s.hub.Publish(Event{Name: "step_appended", Data: stepAppendedPayload{RunID: id, Step: sv}}, TopicRun(id))
}

func (s *Server) publishChunk(id string, turn int, kind, delta string) {
	s.hub.Publish(Event{Name: "chunk", Data: chunkPayload{RunID: id, Turn: turn, Kind: kind, Delta: delta}}, TopicRun(id))
}

// ─── SSE endpoint ─────────────────────────────────────────────────────────────

// handleEvents serves GET /api/events?topics=a,b,c. One connection multiplexes
// every requested topic.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	topics := parseTopics(r.URL.Query().Get("topics"))
	if len(topics) == 0 {
		http.Error(w, "topics required", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	sub, cancel := s.hub.Subscribe(topics...)
	defer cancel()
	rc := http.NewResponseController(w)

	// Open the stream so proxies flush headers immediately.
	if err := writeRaw(w, rc, []byte(": connected\n\n")); err != nil {
		return
	}

	heartbeat := time.NewTicker(sseHeartbeat)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-sub:
			if !ok {
				return
			}
			if err := writeEvent(w, rc, ev); err != nil {
				logSSEError(r, err)
				return
			}
		case <-heartbeat.C:
			if err := writeRaw(w, rc, []byte(": ping\n\n")); err != nil {
				logSSEError(r, err)
				return
			}
		}
	}
}

// parseTopics maps the comma-separated ?topics= value onto Topic values. Any
// non-empty token is accepted (run:<id> topics are opaque), empties dropped.
func parseTopics(v string) []Topic {
	var out []Topic
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, Topic(part))
		}
	}
	return out
}

func writeEvent(w http.ResponseWriter, rc *http.ResponseController, ev Event) error {
	data, err := json.Marshal(ev.Data)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	buf.WriteString("event: ")
	buf.WriteString(ev.Name)
	buf.WriteString("\ndata: ")
	buf.Write(data)
	buf.WriteString("\n\n")
	return writeRaw(w, rc, buf.Bytes())
}

// writeRaw writes b under a per-write deadline, then clears the deadline so the
// connection may sit idle until the next event.
func writeRaw(w http.ResponseWriter, rc *http.ResponseController, b []byte) error {
	if err := rc.SetWriteDeadline(time.Now().Add(sseWriteTimeout)); err != nil &&
		!errors.Is(err, http.ErrNotSupported) {
		return err
	}
	if _, err := w.Write(b); err != nil {
		return err
	}
	if err := rc.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	return rc.SetWriteDeadline(time.Time{})
}

// logSSEError reports stream failures that are NOT the client simply going away.
func logSSEError(r *http.Request, err error) {
	if r.Context().Err() != nil {
		return // client disconnected; expected churn
	}
	log.Printf("sse: %s %s: write failed: %v", r.Method, r.URL.Path, err)
}
