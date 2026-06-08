package web

import (
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"strings"
	"time"
)

// ─── Hierarchical timeline ────────────────────────────────────────────────────

// ToolCallNode pairs a tool_call step with its matching tool_result.
type ToolCallNode struct {
	Call   TimelineStep
	Result *TimelineStep
}

// HasError reports whether this tool call carries a recognised error
// signal: an explicit Err field on the result, a JSON envelope that the
// agent commonly emits for failed tools (`{"ok":false,...}`, `{"error":...}`,
// `{"status":"error"}`), or a leading "error:" / "Error:" prefix in the
// result text. Tool results that look like an error highlight the node in
// the danger palette so operators can scan a noisy trace and spot them.
func (n ToolCallNode) HasError() bool {
	if n.Result == nil {
		return false
	}
	if n.Result.Err != "" {
		return true
	}
	return looksLikeToolError(n.Result.Content)
}

// looksLikeToolError uses cheap heuristics — no full JSON parse — to flag
// tool results that the model and the loop should treat as failures even
// though the wire protocol doesn't surface a dedicated error channel.
func looksLikeToolError(content string) bool {
	t := strings.TrimSpace(content)
	if t == "" {
		return false
	}
	low := strings.ToLower(t)
	switch {
	case strings.HasPrefix(low, "error:"),
		strings.HasPrefix(low, "error "),
		strings.HasPrefix(low, "\"error\""),
		strings.HasPrefix(low, "{\"error\""),
		strings.HasPrefix(low, "fatal:"):
		return true
	}
	if strings.HasPrefix(t, "{") {
		// Cheap structural check — avoids unmarshalling every tool result.
		if strings.Contains(low, "\"ok\":false") ||
			strings.Contains(low, "\"status\":\"error\"") ||
			strings.Contains(low, "\"status\":\"failed\"") ||
			strings.Contains(low, "\"success\":false") {
			return true
		}
	}
	return false
}

// TurnNode aggregates everything that happened in one agentic turn.
type TurnNode struct {
	Index     int
	StartAt   time.Time
	LLMCall   *TimelineStep
	ToolNodes []ToolCallNode
	Final     *TimelineStep
	Error     *TimelineStep
	Reasoning string
	AssistantText string
	HasTokens     bool
	InTokens      int
	OutTokens     int
	CachedToks    int

	// Provider / Model / Fallback summarise the LLM call that drove
	// this turn. Populated from the StepKindLLMResponse event the
	// framework emits after every call (or from any other usage-bearing
	// step that carries the same fields). Empty on legacy traces that
	// pre-date the multiprovider chain.
	Provider string
	Model    string
	Fallback bool

	// APIKeySuffix is the "****xxxx" surface of the key that served the
	// dominant chunk of this turn — propagated from StepKindLLMResponse
	// or, when present, from any individual chunk that carries it.
	// Empty for keyless providers and legacy traces.
	APIKeySuffix string

	// ChunkAttribution captures the per-fragment (Provider, Model, Key)
	// breakdown when more than one (provider, model, key) tuple
	// contributed chunks to this turn — used by the trace UI to surface
	// multi-source streaming. Empty when every chunk in the turn shares
	// the turn-level provenance (the common case).
	ChunkAttribution []ChunkSource
}

// ChunkSource counts how many streaming_chunk steps in a turn came from
// one (provider, model, key) tuple. Surfaced by the trace UI only when
// a single turn drew chunks from multiple tuples — otherwise the
// turn-level Provider / Model / APIKeySuffix already tells the story.
type ChunkSource struct {
	Provider     string
	Model        string
	APIKeySuffix string
	Fallback     bool
	Count        int
}

// EndAt returns the timestamp when the turn finished — either the Final step,
// the last tool result, or the turn's start if nothing else happened.
func (t TurnNode) EndAt() time.Time {
	if t.Final != nil {
		return t.Final.At
	}
	if t.Error != nil {
		return t.Error.At
	}
	for i := len(t.ToolNodes) - 1; i >= 0; i-- {
		if t.ToolNodes[i].Result != nil {
			return t.ToolNodes[i].Result.At
		}
	}
	return t.StartAt
}

// RunTimeline is the hierarchical view of a run consumed by templ components.
type RunTimeline struct {
	SystemPrompt *TimelineStep
	UserInput    *TimelineStep
	Turns        []TurnNode
	StartAt      time.Time
	EndAt        time.Time
}

// Duration returns the wall-clock duration of the run (or elapsed so far).
func (t RunTimeline) Duration() time.Duration {
	if t.StartAt.IsZero() {
		return 0
	}
	end := t.EndAt
	if end.IsZero() {
		end = time.Now()
	}
	return end.Sub(t.StartAt)
}

// SpanGeom is the position+width of a span on a gantt strip, as percentages.
type SpanGeom struct {
	Offset float64
	Width  float64
}

// SpanGeom returns offset+width for a span [from,to] relative to the run.
func (t RunTimeline) SpanGeom(from, to time.Time) SpanGeom {
	d := t.Duration()
	if d <= 0 || from.IsZero() {
		return SpanGeom{}
	}
	if to.IsZero() {
		to = time.Now()
	}
	total := float64(d)
	offset := float64(from.Sub(t.StartAt)) / total * 100
	width := float64(to.Sub(from)) / total * 100
	if offset < 0 {
		offset = 0
	}
	if width < 0.5 {
		width = 0.5
	}
	if offset+width > 100 {
		width = 100 - offset
	}
	return SpanGeom{Offset: offset, Width: width}
}

// BuildTimeline groups raw steps into a hierarchy.
func BuildTimeline(steps []TimelineStep) RunTimeline {
	tl := RunTimeline{}
	if len(steps) == 0 {
		return tl
	}
	tl.StartAt = steps[0].At
	tl.EndAt = steps[len(steps)-1].At

	byTurn := make(map[int]*TurnNode)
	turnOrder := []int{}

	for i := range steps {
		s := steps[i]
		if s.Kind == StepKindSystemPrompt {
			sp := s
			tl.SystemPrompt = &sp
			continue
		}
		if s.Kind == StepKindUserInput {
			ui := s
			tl.UserInput = &ui
			continue
		}
		t, ok := byTurn[s.Turn]
		if !ok {
			t = &TurnNode{Index: s.Turn, StartAt: s.At}
			byTurn[s.Turn] = t
			turnOrder = append(turnOrder, s.Turn)
		}
		if s.InputTokens > 0 || s.OutputTokens > 0 {
			t.HasTokens = true
			if s.InputTokens > t.InTokens {
				t.InTokens = s.InputTokens
			}
			if s.OutputTokens > t.OutTokens {
				t.OutTokens = s.OutputTokens
			}
			if s.CachedTokens > t.CachedToks {
				t.CachedToks = s.CachedTokens
			}
		}
		// Provenance lifts from any step that carries it (most often
		// StepKindLLMResponse). Later usage-bearing steps in the same
		// turn carry the same identity; first-non-empty wins so we
		// don't flip a populated value on a subsequent zero.
		if t.Provider == "" && s.Provider != "" {
			t.Provider = s.Provider
		}
		if t.Model == "" && s.Model != "" {
			t.Model = s.Model
		}
		if t.APIKeySuffix == "" && s.APIKeySuffix != "" {
			t.APIKeySuffix = s.APIKeySuffix
		}
		if s.Fallback {
			t.Fallback = true
		}
		switch s.Kind {
		case StepKindLLMCall:
			marker := s
			t.LLMCall = &marker
		case StepKindLLMResponse:
			// The post-call provenance event. We already lifted its
			// Provider / Model / Fallback / APIKeySuffix above. The
			// Content field carries the full assistant text for the
			// turn — use it as the AssistantText fallback when
			// individual streaming_chunk steps have been stripped
			// (loaded-from-disk runs).
			if t.AssistantText == "" && s.Content != "" {
				t.AssistantText = s.Content
			}
		case StepKindToolCall:
			t.ToolNodes = append(t.ToolNodes, ToolCallNode{Call: s})
		case StepKindToolResult:
			matched := false
			for i := range t.ToolNodes {
				if t.ToolNodes[i].Result != nil {
					continue
				}
				if t.ToolNodes[i].Call.ToolCallID != "" && t.ToolNodes[i].Call.ToolCallID == s.ToolCallID {
					r := s
					t.ToolNodes[i].Result = &r
					matched = true
					break
				}
			}
			if !matched {
				for i := range t.ToolNodes {
					if t.ToolNodes[i].Result == nil {
						r := s
						t.ToolNodes[i].Result = &r
						matched = true
						break
					}
				}
			}
		case StepKindFinal:
			f := s
			t.Final = &f
		case StepKindError:
			e := s
			t.Error = &e
		case StepKindReasoning:
			t.Reasoning += s.Content
		case StepKindStreamingChunk:
			t.AssistantText += s.Content
			recordChunkSource(t, s)
		}
	}
	for _, idx := range turnOrder {
		turn := byTurn[idx]
		// Collapse the per-chunk attribution map when every fragment
		// shared the same provenance: a single-source turn doesn't need
		// the multi-source breakdown surfaced in the UI.
		if len(turn.ChunkAttribution) <= 1 {
			turn.ChunkAttribution = nil
		}
		tl.Turns = append(tl.Turns, *turn)
	}
	return tl
}

// recordChunkSource bumps the per-(provider, model, key) counter on the
// turn when a streaming_chunk carries identity. No-op for legacy chunks
// without provenance fields. Buckets are keyed by the tuple itself so
// repeated chunks from one source collapse to one entry.
func recordChunkSource(t *TurnNode, s TimelineStep) {
	if s.Provider == "" && s.Model == "" && s.APIKeySuffix == "" {
		return
	}
	for i, src := range t.ChunkAttribution {
		if src.Provider == s.Provider && src.Model == s.Model && src.APIKeySuffix == s.APIKeySuffix && src.Fallback == s.Fallback {
			t.ChunkAttribution[i].Count++
			return
		}
	}
	t.ChunkAttribution = append(t.ChunkAttribution, ChunkSource{
		Provider:     s.Provider,
		Model:        s.Model,
		APIKeySuffix: s.APIKeySuffix,
		Fallback:     s.Fallback,
		Count:        1,
	})
}

// ─── View-model types used by templ components ───────────────────────────────

type DashboardData struct {
	TotalRuns   int
	TotalCost   float64
	TotalTokens int
	AvgTurns    float64
	Recent      []*RunRecord
}

type SidebarData struct {
	Groups       []SessionGroup
	Loose        []*RunRecord
	Selected     *RunRecord
	Filter       string
	Query        string
	CountAll     int
	CountRun     int
	CountDone    int
	CountError   int
	CountUnknown int
	// Rollups maps every visible run ID to its cost rollup (own + sub-agents)
	// so cards can show the aggregated total without re-walking the store.
	Rollups map[string]CostRollup
}

type SessionGroup struct {
	ID         string
	Project    string
	Runs       []*RunRecord
	Roots      []*RunNode
	StartedAt  time.Time
	EndedAt    time.Time
	TotalUSD   float64
	HasRunning bool
	HasError   bool
}

type RunNode struct {
	Run      *RunRecord
	Children []*RunNode
}

func (g SessionGroup) Short() string { return ShortID(g.ID) }

func (g SessionGroup) Duration() time.Duration {
	if g.StartedAt.IsZero() {
		return 0
	}
	end := g.EndedAt
	if end.IsZero() || g.HasRunning {
		end = time.Now()
	}
	return end.Sub(g.StartedAt)
}

func (g SessionGroup) Contains(runID string) bool {
	for _, r := range g.Runs {
		if r.ID == runID {
			return true
		}
	}
	return false
}

type DetailData struct {
	Run      *RunRecord
	Timeline RunTimeline
	Live     bool
	// SpawnedByToolCall maps a tool call ID to the sub-agent runs it spawned,
	// each carrying its own timeline + nested children so the trace can expand
	// the whole sub-tree inline instead of navigating away.
	SpawnedByToolCall map[string][]*SpawnedRun
	// RunRollup is this run's cost/tokens including all descendant sub-agents.
	RunRollup CostRollup
}

type RunsViewData struct {
	Sidebar SidebarData
	Detail  *DetailData
}

// ─── Time range filter ────────────────────────────────────────────────────────

type TimeRange struct {
	Since string
	From  string
	To    string
}

func (tr TimeRange) Active() bool { return tr.Since != "" && tr.Since != "all" }

func SinceOptions() []struct{ Value, Label string } {
	return []struct{ Value, Label string }{
		{"15m", "15 min"},
		{"1h", "1 hour"},
		{"24h", "24 hours"},
		{"custom", "custom"},
	}
}

// ─── Chat view models ────────────────────────────────────────────────────────

type ChatViewData struct {
	Sidebar ChatSidebarData
	Detail  *DetailData
}

type ChatSidebarData struct {
	Conversations []ChatConversation
	Selected      *RunRecord
	Filter        string
	Query         string
	CountAll      int
	CountRun      int
	CountDone     int
	CountError    int
	CountUnknown  int
}

type ChatConversation struct {
	ID         string
	ShortID    string
	Project    string
	Messages   []ChatMessage
	TotalUSD   float64
	HasRunning bool
	HasError   bool
}

type ChatMessage struct {
	Run         *RunRecord
	Association ChatAssociation
	Text        string
	StatusClass string
	// Model is a compact label of the model(s) this run used (RunModelLabel).
	Model string
	// Rollup is the run's cost/tokens including spawned sub-agents.
	Rollup CostRollup
	// SubAgentCount / SubAgentRunning summarise the sub-agent runs this run
	// spawned, so the agent bubble can flag them without opening the trace.
	SubAgentCount   int
	SubAgentRunning int
}

type ChatAssociation string

const (
	ChatUser  ChatAssociation = "user"
	ChatAgent ChatAssociation = "agent"
)

// ─── Presentation helpers (used by templ components) ─────────────────────────

func PrettyDuration(d time.Duration) string {
	switch {
	case d < time.Microsecond:
		return fmt.Sprintf("%dns", d.Nanoseconds())
	case d < time.Millisecond:
		return fmt.Sprintf("%dµs", d.Microseconds())
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.2fs", d.Seconds())
	default:
		return d.Round(time.Second).String()
	}
}

func ShortID(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

func ArgsPreview(s string) string {
	t := strings.TrimSpace(s)
	if t == "" {
		return ""
	}
	var v any
	if err := json.Unmarshal([]byte(t), &v); err != nil {
		return Truncate(t, 80)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return Truncate(t, 80)
	}
	return Truncate(string(b), 80)
}

func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func PrettyJSON(s string) template.HTML {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return ""
	}
	if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
		return template.HTML(`<span class="j-x">` + html.EscapeString(s) + `</span>`)
	}
	var v any
	if err := json.Unmarshal([]byte(trimmed), &v); err != nil {
		return template.HTML(`<span class="j-x">` + html.EscapeString(s) + `</span>`)
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return template.HTML(`<span class="j-x">` + html.EscapeString(s) + `</span>`)
	}
	return template.HTML(colorizeJSON(string(b)))
}

func colorizeJSON(s string) string {
	var b strings.Builder
	n := len(s)
	i := 0

	emitString := func(j *int) {
		start := *j
		(*j)++
		for *j < n {
			if s[*j] == '\\' && *j+1 < n {
				*j += 2
				continue
			}
			if s[*j] == '"' {
				(*j)++
				break
			}
			(*j)++
		}
		tok := s[start:*j]
		k := *j
		for k < n && (s[k] == ' ' || s[k] == '\t' || s[k] == '\n' || s[k] == '\r') {
			k++
		}
		class := "j-s"
		if k < n && s[k] == ':' {
			class = "j-k"
		}
		fmt.Fprintf(&b, `<span class="%s">%s</span>`, class, html.EscapeString(tok))
	}
	emitLiteral := func(j *int, lit, class string) bool {
		if strings.HasPrefix(s[*j:], lit) {
			fmt.Fprintf(&b, `<span class="%s">%s</span>`, class, lit)
			*j += len(lit)
			return true
		}
		return false
	}
	emitNumber := func(j *int) {
		start := *j
		if s[*j] == '-' || s[*j] == '+' {
			(*j)++
		}
		for *j < n && (s[*j] >= '0' && s[*j] <= '9' || s[*j] == '.' || s[*j] == 'e' || s[*j] == 'E' || s[*j] == '+' || s[*j] == '-') {
			(*j)++
		}
		fmt.Fprintf(&b, `<span class="j-n">%s</span>`, s[start:*j])
	}
	for i < n {
		c := s[i]
		switch {
		case c == '"':
			emitString(&i)
		case c == 't':
			if !emitLiteral(&i, "true", "j-b") {
				b.WriteByte(c)
				i++
			}
		case c == 'f':
			if !emitLiteral(&i, "false", "j-b") {
				b.WriteByte(c)
				i++
			}
		case c == 'n':
			if !emitLiteral(&i, "null", "j-b") {
				b.WriteByte(c)
				i++
			}
		case c == '-' || (c >= '0' && c <= '9'):
			emitNumber(&i)
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

var _ = fmt.Sprintf
