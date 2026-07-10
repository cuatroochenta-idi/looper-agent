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

// EnvIngestToken carries the bearer token the panel's /ingest endpoint
// requires when the panel runs with auth enabled. Empty (the default) sends
// no Authorization header — matching an auth-less panel.
const EnvIngestToken = "LOOPER_INGEST_TOKEN"

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
	ParentRunID      string          `json:"parent_run_id,omitempty"`       // empty for top-level runs
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
	Kind             string `json:"kind"`
	Turn             int    `json:"turn"`
	Content          string `json:"content,omitempty"`
	ToolName         string `json:"tool_name,omitempty"`
	ToolArgs         string `json:"tool_args,omitempty"`
	ToolCallID       string `json:"tool_call_id,omitempty"`
	Err              string `json:"err,omitempty"`
	InputTokens      int    `json:"input_tokens,omitempty"`
	OutputTokens     int    `json:"output_tokens,omitempty"`
	CachedTokens     int    `json:"cached_tokens,omitempty"`
	CacheWriteTokens int    `json:"cache_write_tokens,omitempty"`

	// Provider / Model: provenance for usage-bearing steps so trace
	// consumers can attribute each turn to the right (provider, model).
	// Empty on non-LLM steps.
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`

	// Fallback is set when the LLM call backing this step came via a
	// non-primary FailoverProvider inner.
	Fallback bool `json:"fallback,omitempty"`

	// APIKeySuffix is the "****xxxx" surface of the API key that served
	// this step's LLM call. Surfaces on the trace UI so operators can
	// tell apart which of several rotating / chained keys answered.
	// Empty for keyless providers and on non-LLM steps.
	APIKeySuffix string `json:"api_key_suffix,omitempty"`
}

// ProviderStatsData mirrors loop.ProviderStats for wire transport.
type ProviderStatsData struct {
	Provider         string  `json:"provider"`
	Model            string  `json:"model"`
	Calls            int     `json:"calls"`
	FallbackCalls    int     `json:"fallback_calls,omitempty"`
	InputTokens      int     `json:"input_tokens,omitempty"`
	OutputTokens     int     `json:"output_tokens,omitempty"`
	CachedTokens     int     `json:"cached_tokens,omitempty"`
	CacheWriteTokens int     `json:"cache_write_tokens,omitempty"`
	TotalUSD         float64 `json:"total_usd,omitempty"`
	InputUSD         float64 `json:"input_usd,omitempty"`
	OutputUSD        float64 `json:"output_usd,omitempty"`
	CachedUSD        float64 `json:"cached_usd,omitempty"`
	CacheWriteUSD    float64 `json:"cache_write_usd,omitempty"`
	SavingsUSD       float64 `json:"savings_usd,omitempty"`
	Estimated        bool    `json:"estimated,omitempty"`
}

// RunEndData is the payload of a run_end event.
type RunEndData struct {
	Output           string  `json:"output,omitempty"`
	Status           string  `json:"status"`
	Turns            int     `json:"turns"`
	TotalUSD         float64 `json:"total_usd,omitempty"`
	CostEstimated    bool    `json:"cost_estimated,omitempty"`
	InputTokens      int     `json:"input_tokens,omitempty"`
	OutputTokens     int     `json:"output_tokens,omitempty"`
	CachedTokens     int     `json:"cached_tokens,omitempty"`
	CacheWriteTokens int     `json:"cache_write_tokens,omitempty"`
	EndedAt          string  `json:"ended_at"`
	Err              string  `json:"err,omitempty"`

	// Providers is the per-(provider, model) breakdown when the run
	// used a multiprovider chain. Empty when single-provider.
	Providers []ProviderStatsData `json:"providers,omitempty"`

	// FallbackCalls is the total number of LLM calls that hit the
	// failover path during this run.
	FallbackCalls int `json:"fallback_calls,omitempty"`
}

// TraceSink receives trace events in-process. It is the transport port
// behind agent tracing: the default adapter POSTs to LOOPER_TRACE_ENDPOINT,
// while hosts that embed the supervision panel in the same binary (see the
// analytics package) implement TraceSink to skip the HTTP hop entirely.
//
// TraceEvent is called from a dedicated goroutine per run — implementations
// may block briefly but must never panic; observability must not take the
// host program down.
type TraceSink interface {
	TraceEvent(ev TraceEvent)
}

// traceWriter delivers events one-by-one to a transport (HTTP endpoint or an
// in-process TraceSink). Failures are silent — observability must never break
// the host program. Sends happen on a private goroutine to keep agent.Run
// latency stable.
type traceWriter struct {
	deliver          func(TraceEvent) // transport adapter; never nil
	sessionID        string
	parentRunID      string // empty if this is a top-level run
	parentToolCallID string // empty if this run wasn't spawned from a tool call
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
	poster := &httpPoster{
		endpoint:    ep,
		ingestToken: strings.TrimSpace(os.Getenv(EnvIngestToken)),
		client:      &http.Client{Timeout: 5 * time.Second},
	}
	return newTraceWriter(ctx, poster.post, sessionOverride)
}

// newTraceWriterForSink builds a writer that hands events to an in-process
// TraceSink instead of the HTTP endpoint. Same queue/backpressure semantics
// as the HTTP path so the agent loop's latency contract is identical.
func newTraceWriterForSink(ctx context.Context, sink TraceSink, sessionOverride string) *traceWriter {
	if sink == nil {
		return nil
	}
	return newTraceWriter(ctx, sink.TraceEvent, sessionOverride)
}

func newTraceWriter(ctx context.Context, deliver func(TraceEvent), sessionOverride string) *traceWriter {
	sid := strings.TrimSpace(sessionOverride)
	if sid == "" {
		sid = strings.TrimSpace(os.Getenv(EnvSessionID))
	}
	tw := &traceWriter{
		deliver:          deliver,
		sessionID:        sid,
		parentRunID:      ParentRunIDFromContext(ctx),
		parentToolCallID: loop.ParentToolCallIDFromContext(ctx),
		queue:            make(chan TraceEvent, 1024),
		done:             make(chan struct{}),
	}
	go tw.run()
	return tw
}

// httpPoster is the default transport adapter: plain JSON POST per event to
// the panel's /ingest endpoint, bearer-authed when a token is configured.
type httpPoster struct {
	endpoint    string
	ingestToken string
	client      *http.Client
}

func (hp *httpPoster) post(ev TraceEvent) {
	body, err := json.Marshal(ev)
	if err != nil {
		return
	}
	req, err := http.NewRequest("POST", hp.endpoint, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if hp.ingestToken != "" {
		req.Header.Set("Authorization", "Bearer "+hp.ingestToken)
	}
	resp, err := hp.client.Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}
}

func (tw *traceWriter) run() {
	defer close(tw.done)
	for ev := range tw.queue {
		tw.deliver(ev)
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
	// Critical event: give the queue a short window, then deliver inline
	// rather than losing the lifecycle marker. Best-effort and bounded by
	// the transport's own timeout (5s for the HTTP adapter).
	select {
	case tw.queue <- ev:
	case <-time.After(50 * time.Millisecond):
		tw.deliver(ev)
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
		Kind:         string(s.Type),
		Turn:         s.Turn,
		Content:      s.Content,
		ToolName:     s.ToolName,
		ToolArgs:     s.ToolArgs,
		ToolCallID:   s.ToolCallID,
		Provider:     s.ProviderID,
		Model:        s.ModelID,
		Fallback:     s.Fallback,
		APIKeySuffix: s.APIKeySuffix,
	}
	if s.Error != nil {
		out.Err = s.Error.Error()
	}
	if s.Usage != nil {
		out.InputTokens = s.Usage.InputTokens
		out.OutputTokens = s.Usage.OutputTokens
		out.CachedTokens = s.Usage.CachedTokens
		out.CacheWriteTokens = s.Usage.CacheWriteTokens
	}
	return out
}

// providersFromLoop converts the loop's per-(provider, model) breakdown
// to the wire-format payload.
func providersFromLoop(in []loop.ProviderStats) []ProviderStatsData {
	if len(in) == 0 {
		return nil
	}
	out := make([]ProviderStatsData, len(in))
	for i, p := range in {
		out[i] = ProviderStatsData{
			Provider:         p.Provider,
			Model:            p.Model,
			Calls:            p.Calls,
			FallbackCalls:    p.FallbackCalls,
			InputTokens:      p.Usage.InputTokens,
			OutputTokens:     p.Usage.OutputTokens,
			CachedTokens:     p.Usage.CachedTokens,
			CacheWriteTokens: p.Usage.CacheWriteTokens,
			TotalUSD:         p.Cost.TotalUSD,
			InputUSD:         p.Cost.InputUSD,
			OutputUSD:        p.Cost.OutputUSD,
			CachedUSD:        p.Cost.CachedUSD,
			CacheWriteUSD:    p.Cost.CacheWriteUSD,
			SavingsUSD:       p.Cost.SavingsUSD,
			Estimated:        p.Cost.Estimated,
		}
	}
	return out
}
