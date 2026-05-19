package looper

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/cuatroochenta-idi/looper-agent/loop"
)

// EnvTraceEndpoint is the environment variable read by NewAgent / Agent.Run
// to discover a trace collector endpoint. When set, every run automatically
// streams events to it via plain JSON POST. Empty disables tracing.
const EnvTraceEndpoint = "LOOPER_TRACE_ENDPOINT"

// EnvSessionID groups multiple agent.Run() calls from the same process into a
// single session in the debug panel. Typically injected by `looper serve --`
// when it execs a wrapped child; manually settable too.
const EnvSessionID = "LOOPER_SESSION_ID"

// parentRunIDKey is the context key used to propagate the in-flight runID
// from a parent agent down to any sub-agent spawned inside a tool function.
// Sub-agents discover the parent by reading this key off ctx — that way
// nesting works purely by passing context, no env-var plumbing required.
type parentRunIDKey struct{}

// ParentRunIDFromContext returns the runID of the agent that owns this
// context, if any. Tools that spawn sub-agents don't need to call this —
// they just forward ctx to subAgent.Run / Iterate and the tracer picks it
// up automatically.
func ParentRunIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(parentRunIDKey{}).(string); ok {
		return v
	}
	return ""
}

// contextWithRunID stamps the current runID on ctx so that any sub-agent
// invoked from a tool can recover it via ParentRunIDFromContext.
func contextWithRunID(ctx context.Context, runID string) context.Context {
	return context.WithValue(ctx, parentRunIDKey{}, runID)
}

// TraceEventType identifies the shape of a trace event over the wire.
type TraceEventType string

const (
	TraceRunStart TraceEventType = "run_start"
	TraceStep     TraceEventType = "step"
	TraceRunEnd   TraceEventType = "run_end"
)

// TraceEvent is the wire-format envelope. Backend stores them, replays them,
// and merges them into RunRecords on the panel side.
type TraceEvent struct {
	Type             TraceEventType  `json:"type"`
	RunID            string          `json:"run_id"`
	ParentRunID      string          `json:"parent_run_id,omitempty"`      // empty for top-level runs
	ParentToolCallID string          `json:"parent_tool_call_id,omitempty"` // populated when this run was spawned from a tool call
	SessionID        string          `json:"session_id,omitempty"`
	Ts               time.Time       `json:"ts"`
	Project          string          `json:"project,omitempty"` // CWD basename for grouping
	Data             json.RawMessage `json:"data,omitempty"`
}

// RunStartData is the payload of a run_start event.
type RunStartData struct {
	Input        string `json:"input"`
	SystemPrompt string `json:"system_prompt,omitempty"`
	Model        string `json:"model,omitempty"`
	Provider     string `json:"provider,omitempty"`
	StartedAt    string `json:"started_at"`
}

// StepData mirrors loop.Step for wire transport.
type StepData struct {
	Kind         string `json:"kind"`
	Turn         int    `json:"turn"`
	Content      string `json:"content,omitempty"`
	ToolName     string `json:"tool_name,omitempty"`
	ToolArgs     string `json:"tool_args,omitempty"`
	ToolCallID   string `json:"tool_call_id,omitempty"`
	Err          string `json:"err,omitempty"`
	InputTokens  int    `json:"input_tokens,omitempty"`
	OutputTokens int    `json:"output_tokens,omitempty"`
	CachedTokens int    `json:"cached_tokens,omitempty"`
}

// RunEndData is the payload of a run_end event.
type RunEndData struct {
	Output       string  `json:"output,omitempty"`
	Status       string  `json:"status"`
	Turns        int     `json:"turns"`
	TotalUSD     float64 `json:"total_usd,omitempty"`
	InputTokens  int     `json:"input_tokens,omitempty"`
	OutputTokens int     `json:"output_tokens,omitempty"`
	CachedTokens int     `json:"cached_tokens,omitempty"`
	EndedAt      string  `json:"ended_at"`
	Err          string  `json:"err,omitempty"`
}

// traceWriter posts events one-by-one to the endpoint. Failures are silent —
// observability must never break the host program. Sends happen on a private
// goroutine to keep agent.Run latency stable.
type traceWriter struct {
	endpoint         string
	sessionID        string
	parentRunID      string // empty if this is a top-level run
	parentToolCallID string // empty if this run wasn't spawned from a tool call
	client           *http.Client
	mu               sync.Mutex
	queue            chan TraceEvent
	done             chan struct{}
}

// newTraceWriterFromEnv returns a writer if LOOPER_TRACE_ENDPOINT is set,
// otherwise nil. The ctx supplies both the parent runID (for sub-agents) and
// the parent tool-call ID (for sub-agents spawned from a specific tool call),
// auto-stamped by Agent.Iterate and AgentLoop.executeSingleTool respectively.
// Callers should always be nil-safe on the returned value.
//
// sessionOverride takes precedence over LOOPER_SESSION_ID when non-empty so
// the per-Run WithSessionID option can group traces independently of the
// process-wide env var.
func newTraceWriterFromEnv(ctx context.Context, sessionOverride string) *traceWriter {
	ep := strings.TrimSpace(os.Getenv(EnvTraceEndpoint))
	if ep == "" {
		return nil
	}
	sid := strings.TrimSpace(sessionOverride)
	if sid == "" {
		sid = strings.TrimSpace(os.Getenv(EnvSessionID))
	}
	tw := &traceWriter{
		endpoint:         ep,
		sessionID:        sid,
		parentRunID:      ParentRunIDFromContext(ctx),
		parentToolCallID: loop.ParentToolCallIDFromContext(ctx),
		client:           &http.Client{Timeout: 5 * time.Second},
		queue:            make(chan TraceEvent, 1024),
		done:             make(chan struct{}),
	}
	go tw.run()
	return tw
}

func (tw *traceWriter) run() {
	defer close(tw.done)
	for ev := range tw.queue {
		body, err := json.Marshal(ev)
		if err != nil {
			continue
		}
		req, err := http.NewRequest("POST", tw.endpoint, bytes.NewReader(body))
		if err != nil {
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := tw.client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
	}
}

// send enqueues an event. Drops silently if the queue is full (back-pressure
// must never block the agent loop). run_start and run_end events are critical
// for the panel's run-state machine (without them runs get stuck "running"
// forever) — for those we block briefly to give the worker a chance to drain,
// then post inline as a last resort.
func (tw *traceWriter) send(t TraceEventType, runID string, data any) {
	if tw == nil {
		return
	}
	var raw json.RawMessage
	if data != nil {
		b, err := json.Marshal(data)
		if err == nil {
			raw = b
		}
	}
	ev := TraceEvent{
		Type:             t,
		RunID:            runID,
		ParentRunID:      tw.parentRunID,
		ParentToolCallID: tw.parentToolCallID,
		SessionID:        tw.sessionID,
		Ts:               time.Now(),
		Project:          projectName(),
		Data:             raw,
	}
	critical := t == TraceRunStart || t == TraceRunEnd
	select {
	case tw.queue <- ev:
		return
	default:
	}
	if !critical {
		// step-level event under back-pressure: drop silently so the
		// host program never stalls on observability.
		return
	}
	// Critical event: give the queue a short window, then post inline
	// rather than losing the lifecycle marker.
	select {
	case tw.queue <- ev:
	case <-time.After(50 * time.Millisecond):
		tw.postInline(ev)
	}
}

// postInline issues a one-off HTTP POST for an event that couldn't be
// enqueued. Best-effort and bounded by the client's existing 5s timeout.
func (tw *traceWriter) postInline(ev TraceEvent) {
	body, err := json.Marshal(ev)
	if err != nil {
		return
	}
	req, err := http.NewRequest("POST", tw.endpoint, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := tw.client.Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}
}

// close drains pending events and stops the worker.
func (tw *traceWriter) close() {
	if tw == nil {
		return
	}
	close(tw.queue)
	<-tw.done
}

// projectName returns the basename of the current working directory. The
// receiver uses it to group runs from different projects.
func projectName() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	for i := len(wd) - 1; i >= 0; i-- {
		if wd[i] == '/' || wd[i] == '\\' {
			return wd[i+1:]
		}
	}
	return wd
}

// stepDataFrom converts a loop.Step into the wire-format payload.
func stepDataFrom(s loop.Step) StepData {
	out := StepData{
		Kind:       string(s.Type),
		Turn:       s.Turn,
		Content:    s.Content,
		ToolName:   s.ToolName,
		ToolArgs:   s.ToolArgs,
		ToolCallID: s.ToolCallID,
	}
	if s.Error != nil {
		out.Err = s.Error.Error()
	}
	if s.Usage != nil {
		out.InputTokens = s.Usage.InputTokens
		out.OutputTokens = s.Usage.OutputTokens
		out.CachedTokens = s.Usage.CachedTokens
	}
	return out
}
