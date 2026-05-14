// Example: presentation builder driven by an LLM agent.
//
// Run it standalone:
//
//	export OPENAI_API_KEY=sk-...
//	go run ./examples/08_presentation_builder
//
// Then open http://localhost:8765. Type into the left-hand chat —
// the agent uses tool calls to mutate the deck, and SSE pushes every
// change to the right-hand preview in real time.
//
// What this demo combines
//
//   - A chat sidebar that streams agent messages step-by-step.
//   - A live preview rendered server-side and patched in via datastar SSE.
//   - Seven design tools (the "screen design skill"): add_slide,
//     add_bullets, set_body, add_chart, set_theme, delete_slide, reorder.
//   - Task state: every tool call publishes on a pub/sub bus; any number
//     of browser tabs reflect the same deck.
//   - Multi-turn conversation: the agent's history persists across user
//     turns so it remembers prior slides.
//
// Everything fits in one .go file + one .html template.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/starfederation/datastar-go/datastar"

	"github.com/cuatroochenta-idi/looper-agent/looper"
	"github.com/cuatroochenta-idi/looper-agent/loop"
	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/provider/openai"
	"github.com/cuatroochenta-idi/looper-agent/tool"
)

//go:embed index.html
var indexHTML string

var indexTmpl = template.Must(template.New("index").Parse(indexHTML))

// ─── Deck model ───────────────────────────────────────────────────────────────

type Slide struct {
	Title   string
	Layout  string // "title" | "content" | "two_column" | "chart"
	Bullets []string
	Body    string
	Chart   *Chart
	Figure  *Figure
}

type Chart struct {
	Title  string
	Labels []string
	Values []float64
}

type Figure struct {
	Kind string // "rocket" | "graph" | "team" | "globe" | "lightbulb" | "gears"
}

type Deck struct {
	mu     sync.RWMutex
	Theme  string // "dark" | "light" | "sunset" | "aqua"
	Slides []*Slide
}

func newDeck() *Deck { return &Deck{Theme: "dark", Slides: []*Slide{}} }

func (d *Deck) snapshot() (string, []Slide) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]Slide, len(d.Slides))
	for i, s := range d.Slides {
		c := *s
		// Deep-copy slices so the template can range safely outside the lock.
		c.Bullets = append([]string(nil), s.Bullets...)
		if s.Chart != nil {
			ch := *s.Chart
			ch.Labels = append([]string(nil), s.Chart.Labels...)
			ch.Values = append([]float64(nil), s.Chart.Values...)
			c.Chart = &ch
		}
		if s.Figure != nil {
			f := *s.Figure
			c.Figure = &f
		}
		out[i] = c
	}
	return d.Theme, out
}

func (d *Deck) addSlide(title, layout string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	if layout == "" {
		layout = "content"
	}
	d.Slides = append(d.Slides, &Slide{Title: title, Layout: layout})
	return len(d.Slides) - 1
}

func (d *Deck) inBounds(i int) bool {
	return i >= 0 && i < len(d.Slides)
}

func (d *Deck) addBullets(i int, items []string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.inBounds(i) {
		return fmt.Errorf("slide_index %d out of range [0,%d)", i, len(d.Slides))
	}
	d.Slides[i].Bullets = append(d.Slides[i].Bullets, items...)
	return nil
}

func (d *Deck) setBody(i int, text string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.inBounds(i) {
		return fmt.Errorf("slide_index %d out of range", i)
	}
	d.Slides[i].Body = text
	return nil
}

func (d *Deck) addChart(i int, c Chart) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.inBounds(i) {
		return fmt.Errorf("slide_index %d out of range", i)
	}
	if len(c.Labels) != len(c.Values) {
		return fmt.Errorf("labels (%d) and values (%d) must have the same length", len(c.Labels), len(c.Values))
	}
	d.Slides[i].Chart = &c
	d.Slides[i].Layout = "chart"
	return nil
}

func (d *Deck) setTheme(name string) error {
	switch name {
	case "dark", "light", "sunset", "aqua":
		d.mu.Lock()
		d.Theme = name
		d.mu.Unlock()
		return nil
	}
	return fmt.Errorf("unknown theme %q", name)
}

func (d *Deck) deleteSlide(i int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.inBounds(i) {
		return fmt.Errorf("slide_index %d out of range", i)
	}
	d.Slides = append(d.Slides[:i], d.Slides[i+1:]...)
	return nil
}

func (d *Deck) addFigure(i int, kind string) error {
	switch kind {
	case "rocket", "graph", "team", "globe", "lightbulb", "gears":
	default:
		return fmt.Errorf("unknown figure kind %q", kind)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.inBounds(i) {
		return fmt.Errorf("slide_index %d out of range", i)
	}
	d.Slides[i].Figure = &Figure{Kind: kind}
	return nil
}

func (d *Deck) reorder(order []int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(order) != len(d.Slides) {
		return fmt.Errorf("order length %d != %d slides", len(order), len(d.Slides))
	}
	seen := make(map[int]bool)
	next := make([]*Slide, len(d.Slides))
	for newPos, oldIdx := range order {
		if oldIdx < 0 || oldIdx >= len(d.Slides) || seen[oldIdx] {
			return fmt.Errorf("invalid order: %v", order)
		}
		seen[oldIdx] = true
		next[newPos] = d.Slides[oldIdx]
	}
	d.Slides = next
	return nil
}

// ─── Chat log ─────────────────────────────────────────────────────────────────

type ChatRole string

const (
	RoleUser      ChatRole = "user"
	RoleAssistant ChatRole = "assistant"
	RoleTool      ChatRole = "tool"
	RoleStatus    ChatRole = "status"
	RoleError     ChatRole = "error"
)

type ChatMsg struct {
	Role    ChatRole
	Content string
	Tool    string
	Args    string
	At      time.Time
}

// ─── Tiny pub/sub for SSE wake-ups ────────────────────────────────────────────

type pubsub struct {
	mu   sync.Mutex
	subs map[string]map[chan struct{}]bool
}

func newPubsub() *pubsub { return &pubsub{subs: map[string]map[chan struct{}]bool{}} }

func (p *pubsub) sub(topic string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 4)
	p.mu.Lock()
	if p.subs[topic] == nil {
		p.subs[topic] = map[chan struct{}]bool{}
	}
	p.subs[topic][ch] = true
	p.mu.Unlock()
	return ch, func() {
		p.mu.Lock()
		delete(p.subs[topic], ch)
		p.mu.Unlock()
		close(ch)
	}
}

func (p *pubsub) pub(topic string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for ch := range p.subs[topic] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// ─── App: glues everything together ───────────────────────────────────────────

type App struct {
	deck      *Deck
	msgs      []ChatMsg
	msgsMu    sync.RWMutex
	history   *message.History
	historyMu sync.Mutex
	agent     *looper.Agent
	bus       *pubsub
	busy      int32 // atomic
}

func newApp() *App {
	return &App{
		deck:    newDeck(),
		history: message.NewHistory(),
		bus:     newPubsub(),
	}
}

func (a *App) appendMsg(m ChatMsg) {
	m.At = time.Now()
	a.msgsMu.Lock()
	a.msgs = append(a.msgs, m)
	a.msgsMu.Unlock()
	a.bus.pub("chat")
}

func (a *App) snapshotMsgs() []ChatMsg {
	a.msgsMu.RLock()
	defer a.msgsMu.RUnlock()
	out := make([]ChatMsg, len(a.msgs))
	copy(out, a.msgs)
	return out
}

// ─── Tool input schemas (the "screen design skill") ───────────────────────────

type AddSlideIn struct {
	Title  string `json:"title" jsonschema:"description=Slide heading"`
	Layout string `json:"layout" jsonschema:"description=Visual layout,enum=title,enum=content,enum=two_column,enum=chart"`
}

type AddBulletsIn struct {
	SlideIndex int      `json:"slide_index" jsonschema:"description=0-indexed slide position"`
	Items      []string `json:"items" jsonschema:"description=One bullet per item"`
}

type SetBodyIn struct {
	SlideIndex int    `json:"slide_index"`
	Text       string `json:"text" jsonschema:"description=Paragraph body that appears below the title"`
}

type AddChartIn struct {
	SlideIndex int       `json:"slide_index"`
	Title      string    `json:"chart_title"`
	Labels     []string  `json:"labels"`
	Values     []float64 `json:"values"`
}

type SetThemeIn struct {
	Name string `json:"name" jsonschema:"description=Theme name,enum=dark,enum=light,enum=sunset,enum=aqua"`
}

type DeleteSlideIn struct {
	SlideIndex int `json:"slide_index"`
}

type ReorderIn struct {
	Order []int `json:"order" jsonschema:"description=New ordering as a permutation of slide indices"`
}

type AddFigureIn struct {
	SlideIndex int    `json:"slide_index" jsonschema:"description=0-indexed slide position"`
	Kind       string `json:"kind" jsonschema:"description=Figure kind,enum=rocket,enum=graph,enum=team,enum=globe,enum=lightbulb,enum=gears"`
}

// ─── Build the agent ──────────────────────────────────────────────────────────

const systemPrompt = `You are a slide-deck builder. You MUST drive the UI via tool calls — never reply with plain text only until the deck is fully built.

Mandatory flow on every user request:
1. set_theme — pick dark/light/sunset/aqua.
2. add_slide layout="title" — slide 0 (the title slide).
3. set_body on slide 0 with a one-line tagline.
4. add_slide layout="content" + add_bullets (3-5 items) — repeat for each content slide.
5. If the topic mentions numbers, comparisons, growth, or stats: add_slide layout="chart" + add_chart with realistic illustrative numbers.
6. Sprinkle 1-2 add_figure calls on content slides to make them visual. Kinds: rocket, graph, team, globe, lightbulb, gears. Match the kind to the slide's topic.
7. Only AFTER every tool call is done, reply with one short sentence summarizing the deck.

Slides are 0-indexed. Use the index that add_slide returns. Never invent indices.`

func (a *App) setupAgent(p *openai.Provider) {
	tools := []*tool.Tool{
		tool.MustNewTool(AddSlideIn{},
			func(_ context.Context, in AddSlideIn) (string, error) {
				i := a.deck.addSlide(strings.TrimSpace(in.Title), in.Layout)
				a.bus.pub("deck")
				return fmt.Sprintf("added slide %d (layout=%s)", i, in.Layout), nil
			},
			tool.ToolConfig{Name: "add_slide", Description: "Append a new slide. Returns its 0-indexed position."},
		),
		tool.MustNewTool(AddBulletsIn{},
			func(_ context.Context, in AddBulletsIn) (string, error) {
				if err := a.deck.addBullets(in.SlideIndex, in.Items); err != nil {
					return "", err
				}
				a.bus.pub("deck")
				return fmt.Sprintf("added %d bullets to slide %d", len(in.Items), in.SlideIndex), nil
			},
			tool.ToolConfig{Name: "add_bullets", Description: "Append bullet items to an existing slide.", Parallel: true},
		),
		tool.MustNewTool(SetBodyIn{},
			func(_ context.Context, in SetBodyIn) (string, error) {
				if err := a.deck.setBody(in.SlideIndex, in.Text); err != nil {
					return "", err
				}
				a.bus.pub("deck")
				return fmt.Sprintf("set body on slide %d", in.SlideIndex), nil
			},
			tool.ToolConfig{Name: "set_body", Description: "Set the paragraph body of a slide. Use for title slides and intros."},
		),
		tool.MustNewTool(AddChartIn{},
			func(_ context.Context, in AddChartIn) (string, error) {
				if err := a.deck.addChart(in.SlideIndex, Chart{Title: in.Title, Labels: in.Labels, Values: in.Values}); err != nil {
					return "", err
				}
				a.bus.pub("deck")
				return fmt.Sprintf("added chart with %d series to slide %d", len(in.Values), in.SlideIndex), nil
			},
			tool.ToolConfig{Name: "add_chart", Description: "Attach a horizontal bar chart to a slide. Switches the slide's layout to 'chart'."},
		),
		tool.MustNewTool(SetThemeIn{},
			func(_ context.Context, in SetThemeIn) (string, error) {
				if err := a.deck.setTheme(in.Name); err != nil {
					return "", err
				}
				a.bus.pub("deck")
				return fmt.Sprintf("theme=%s", in.Name), nil
			},
			tool.ToolConfig{Name: "set_theme", Description: "Pick the visual theme for the whole deck."},
		),
		tool.MustNewTool(DeleteSlideIn{},
			func(_ context.Context, in DeleteSlideIn) (string, error) {
				if err := a.deck.deleteSlide(in.SlideIndex); err != nil {
					return "", err
				}
				a.bus.pub("deck")
				return fmt.Sprintf("deleted slide %d", in.SlideIndex), nil
			},
			tool.ToolConfig{Name: "delete_slide", Description: "Remove a slide by index. Subsequent indices shift down."},
		),
		tool.MustNewTool(ReorderIn{},
			func(_ context.Context, in ReorderIn) (string, error) {
				if err := a.deck.reorder(in.Order); err != nil {
					return "", err
				}
				a.bus.pub("deck")
				return fmt.Sprintf("reordered to %v", in.Order), nil
			},
			tool.ToolConfig{Name: "reorder_slides", Description: "Reorder the deck. Pass the new ordering as a permutation of current indices."},
		),
		tool.MustNewTool(AddFigureIn{},
			func(_ context.Context, in AddFigureIn) (string, error) {
				if err := a.deck.addFigure(in.SlideIndex, in.Kind); err != nil {
					return "", err
				}
				a.bus.pub("deck")
				return fmt.Sprintf("added %s figure to slide %d", in.Kind, in.SlideIndex), nil
			},
			tool.ToolConfig{Name: "add_figure", Description: "Attach a stylized inline-SVG figure to a slide. Kinds: rocket, graph, team, globe, lightbulb, gears."},
		),
	}
	comps := make([]any, len(tools))
	for i, t := range tools {
		comps[i] = t
	}
	a.agent = looper.MustNewAgent(p, systemPrompt, comps...)
}

// ─── HTTP handlers ────────────────────────────────────────────────────────────

func (a *App) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = indexTmpl.Execute(w, nil)
}

// chatInput is the datastar signal payload for the chat form.
type chatInput struct {
	Input string `json:"input"`
}

// handleChat reads the $input signal, appends a user message, and runs the
// agent. Tool calls mutate the deck; SSE clients on /sse/deck and /sse/chat
// pick up patches automatically.
func (a *App) handleChat(w http.ResponseWriter, r *http.Request) {
	var in chatInput
	_ = datastar.ReadSignals(r, &in)
	input := strings.TrimSpace(in.Input)
	if input == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !atomic.CompareAndSwapInt32(&a.busy, 0, 1) {
		a.appendMsg(ChatMsg{Role: RoleError, Content: "Agent is already busy — wait for the current turn."})
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Append the user msg synchronously so the SSE push includes it before
	// the agent starts thinking.
	a.appendMsg(ChatMsg{Role: RoleUser, Content: input})

	go a.runAgent(input)

	w.WriteHeader(http.StatusNoContent)
}

// runAgent drives one turn end-to-end: feeds the user input + restored history
// to agent.Iterate, turns each loop.Step into a chat message, and lets the
// tool functions (closures over the deck) update the preview in-line.
func (a *App) runAgent(input string) {
	defer atomic.StoreInt32(&a.busy, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	a.historyMu.Lock()
	hist := a.history
	a.historyMu.Unlock()

	iter := a.agent.Iterate(ctx, input, looper.WithHistory(hist))
	for step := range iter.Next() {
		switch step.Type {
		case loop.StepLLMCall:
			// fired before the LLM responds; the chat already shows "thinking".
		case loop.StepToolCall:
			a.appendMsg(ChatMsg{
				Role:    RoleTool,
				Content: "→ " + step.ToolName,
				Tool:    step.ToolName,
				Args:    step.ToolArgs,
			})
		case loop.StepToolResult:
			// noop — the tool function already published a deck update.
		case loop.StepFinalResponse:
			if strings.TrimSpace(step.Content) == "" {
				a.appendMsg(ChatMsg{
					Role:    RoleStatus,
					Content: "(agent stopped after tool calls without a closing message)",
				})
			} else {
				a.appendMsg(ChatMsg{Role: RoleAssistant, Content: step.Content})
			}
		case loop.StepError:
			msg := "(agent error)"
			if step.Error != nil {
				msg = step.Error.Error()
			}
			a.appendMsg(ChatMsg{Role: RoleError, Content: msg})
		}
	}
	res := iter.Result()
	a.historyMu.Lock()
	if res.History != nil {
		a.history = res.History
	}
	a.historyMu.Unlock()
}

// sseDeck pushes a fresh rendered preview every time a tool mutates the deck.
// One persistent connection per browser tab; the pub/sub fan-out lets every
// open tab show the same state.
func (a *App) sseDeck(w http.ResponseWriter, r *http.Request) {
	sse := datastar.NewSSE(w, r)
	sub, cancel := a.bus.sub("deck")
	defer cancel()

	push := func() {
		_ = sse.PatchElements(a.renderDeck(),
			datastar.WithSelector("#deck-preview"),
			datastar.WithMode(datastar.ElementPatchModeInner),
		)
	}
	push()
	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-sub:
			push()
		case <-heartbeat.C:
			push()
		}
	}
}

func (a *App) sseChat(w http.ResponseWriter, r *http.Request) {
	sse := datastar.NewSSE(w, r)
	sub, cancel := a.bus.sub("chat")
	defer cancel()

	push := func() {
		_ = sse.PatchElements(a.renderChat(),
			datastar.WithSelector("#chat-log"),
			datastar.WithMode(datastar.ElementPatchModeInner),
		)
		// Two signals piggy-back on every push:
		//   busy     → the input disables itself while the agent runs.
		//   msgCount → bumped on each push so the chat-log can auto-scroll
		//              to the bottom via data-effect on the client.
		a.msgsMu.RLock()
		n := len(a.msgs)
		a.msgsMu.RUnlock()
		_ = sse.PatchSignals([]byte(fmt.Sprintf(
			`{"busy":%t,"msgCount":%d}`,
			atomic.LoadInt32(&a.busy) == 1, n,
		)))
	}
	push()
	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-sub:
			push()
		case <-heartbeat.C:
			push()
		}
	}
}

// ─── Render helpers ───────────────────────────────────────────────────────────

func (a *App) renderDeck() string {
	theme, slides := a.deck.snapshot()
	var b strings.Builder
	// Wrapper carries a signal map: slide-zoom + per-slide bullet-highlight.
	// We rebuild signals on every full deck render to keep state in sync with
	// the slide count. Existing signals are merged with `data-signals` on this
	// element; datastar deep-merges so prior values for slides still present
	// are preserved.
	fmt.Fprintf(&b, `<div class="deck deck-grid theme-%s" data-signals="{zoomed: -1}">`, htmlEscape(theme))
	if len(slides) == 0 {
		b.WriteString(`<div class="empty col-span-full py-24 text-center text-zinc-400 text-sm">No slides yet. Tell the agent what you want.</div>`)
	}
	// Progress dots (top of preview pane area) when we have slides.
	if len(slides) > 0 {
		b.WriteString(`<div class="deck-dots col-span-full flex items-center justify-center gap-1.5 mb-2">`)
		for i := range slides {
			fmt.Fprintf(&b, `<span class="deck-dot" data-class="{'deck-dot-active': $zoomed === %d}"></span>`, i)
		}
		b.WriteString(`</div>`)
	}
	for i, s := range slides {
		renderSlide(&b, i, s)
	}
	b.WriteString(`</div>`)
	return b.String()
}

func renderSlide(b *strings.Builder, idx int, s Slide) {
	// Stable id keyed on idx+layout+title — used by CSS animation-name so the
	// entry animation doesn't re-trigger on every SSE patch.
	stableKey := fmt.Sprintf("%d-%s", idx, htmlEscape(s.Title))
	fmt.Fprintf(b,
		`<article class="slide layout-%s" data-slide-key="%s" data-signals="{hl%d: -1}" data-class="{'slide-zoomed': $zoomed === %d}">`,
		htmlEscape(s.Layout), stableKey, idx, idx)

	// Slide header: number badge + title + fullscreen toggle button.
	fmt.Fprintf(b, `<header class="slide-head">`)
	fmt.Fprintf(b, `<span class="slide-num">%02d</span>`, idx+1)
	fmt.Fprintf(b, `<h2 class="slide-title">%s</h2>`, htmlEscape(s.Title))
	fmt.Fprintf(b,
		`<button type="button" class="slide-zoom-btn" title="zoom this slide" data-on:click="$zoomed = ($zoomed === %d ? -1 : %d)" data-on:keydown__window="evt.key === 'Escape' && ($zoomed = -1)">`,
		idx, idx)
	b.WriteString(`<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M15 3h6v6"/><path d="M9 21H3v-6"/><path d="M21 3l-7 7"/><path d="M3 21l7-7"/></svg>`)
	b.WriteString(`</button>`)
	b.WriteString(`</header>`)

	// Body. two_column layout splits bullets/body+figure into two columns.
	isTwoCol := s.Layout == "two_column"
	if isTwoCol {
		b.WriteString(`<div class="slide-body slide-twocol">`)
		// Left column: body text + bullets.
		b.WriteString(`<div class="slide-col">`)
		if s.Body != "" {
			fmt.Fprintf(b, `<p class="slide-body-text">%s</p>`, htmlEscape(s.Body))
		}
		renderBullets(b, idx, s.Bullets)
		b.WriteString(`</div>`)
		// Right column: figure or chart.
		b.WriteString(`<div class="slide-col slide-col-visual">`)
		if s.Figure != nil {
			fmt.Fprintf(b, `<div class="figure figure-%s">%s</div>`, htmlEscape(s.Figure.Kind), figureSVG(s.Figure.Kind))
		}
		if s.Chart != nil {
			renderChart(b, *s.Chart)
		}
		b.WriteString(`</div>`)
		b.WriteString(`</div>`)
	} else {
		b.WriteString(`<div class="slide-body">`)
		if s.Body != "" {
			fmt.Fprintf(b, `<p class="slide-body-text">%s</p>`, htmlEscape(s.Body))
		}
		// Figure + bullets share the row when both exist.
		hasFigure := s.Figure != nil
		hasBullets := len(s.Bullets) > 0
		if hasFigure && hasBullets {
			b.WriteString(`<div class="slide-fig-row">`)
			renderBullets(b, idx, s.Bullets)
			fmt.Fprintf(b, `<div class="figure figure-%s">%s</div>`, htmlEscape(s.Figure.Kind), figureSVG(s.Figure.Kind))
			b.WriteString(`</div>`)
		} else {
			if hasFigure {
				fmt.Fprintf(b, `<div class="figure figure-%s">%s</div>`, htmlEscape(s.Figure.Kind), figureSVG(s.Figure.Kind))
			}
			if hasBullets {
				renderBullets(b, idx, s.Bullets)
			}
		}
		if s.Chart != nil {
			renderChart(b, *s.Chart)
		}
		b.WriteString(`</div>`)
	}
	b.WriteString(`</article>`)
}

func renderBullets(b *strings.Builder, slideIdx int, bullets []string) {
	if len(bullets) == 0 {
		return
	}
	b.WriteString(`<ul class="slide-bullets">`)
	for i, item := range bullets {
		fmt.Fprintf(b,
			`<li class="slide-bullet" data-class="{'bullet-hl': $hl%d === %d}" data-on:click="$hl%d = ($hl%d === %d ? -1 : %d)">%s</li>`,
			slideIdx, i, slideIdx, slideIdx, i, i, htmlEscape(item))
	}
	b.WriteString(`</ul>`)
}

func renderChart(b *strings.Builder, c Chart) {
	var max float64
	for _, v := range c.Values {
		if v > max {
			max = v
		}
	}
	if max == 0 {
		max = 1
	}
	fmt.Fprintf(b, `<div class="chart"><div class="chart-title">%s</div><div class="chart-bars">`,
		htmlEscape(c.Title))
	for i, v := range c.Values {
		label := ""
		if i < len(c.Labels) {
			label = c.Labels[i]
		}
		pct := (v / max) * 100
		// Use --bar-w custom property + animation so bars grow from 0 → pct on
		// first render.
		fmt.Fprintf(b,
			`<div class="bar-row"><span class="bar-label">%s</span><span class="bar-track"><span class="bar-fill" style="--bar-w:%.2f%%"></span></span><span class="bar-value">%g</span></div>`,
			htmlEscape(label), pct, v)
	}
	b.WriteString(`</div></div>`)
}

func (a *App) renderChat() string {
	msgs := a.snapshotMsgs()
	var b strings.Builder
	if len(msgs) == 0 {
		b.WriteString(`<div class="chat-empty flex flex-col gap-3 py-10 px-3 text-center">`)
		b.WriteString(`<div class="text-zinc-400 text-sm">What kind of deck would you like to build?</div>`)
		b.WriteString(`<div class="flex flex-col gap-2 mt-2">`)
		examples := []string{
			"Build a 5-slide investor pitch for a coffee subscription startup. Sunset theme.",
			"Make a 4-slide product roadmap for a fintech app. Include a chart. Aqua theme.",
			"Pitch a SaaS that helps small teams ship faster. Add a rocket figure. Dark theme.",
		}
		for _, ex := range examples {
			// Use Go template-style escape inside data-on:click string literal:
			// the value is single-quoted, so any ' in the prompt has to be
			// escaped — none of our prompts use one.
			fmt.Fprintf(&b,
				`<button type="button" class="prompt-chip text-left text-xs px-3 py-2 rounded-lg bg-zinc-800/60 hover:bg-zinc-700/70 border border-zinc-700/60 text-zinc-300 transition" data-on:click="$input = '%s'">%s</button>`,
				htmlEscape(ex), htmlEscape(ex))
		}
		b.WriteString(`</div></div>`)
		return b.String()
	}
	for _, m := range msgs {
		switch m.Role {
		case RoleUser:
			fmt.Fprintf(&b, `<div class="msg-row msg-row-user flex justify-end">`)
			b.WriteString(`<div class="flex flex-col items-end gap-0.5 max-w-[85%]">`)
			fmt.Fprintf(&b, `<span class="text-[10px] uppercase tracking-wider text-indigo-300/70 font-semibold">you</span>`)
			fmt.Fprintf(&b, `<div class="msg-bubble msg-bubble-user px-3 py-2 rounded-2xl rounded-tr-sm bg-indigo-500/90 text-white text-[13px] leading-snug whitespace-pre-wrap break-words shadow-sm">%s</div>`, htmlEscape(m.Content))
			fmt.Fprintf(&b, `<span class="text-[10px] text-zinc-500 font-mono">%s</span>`, m.At.Format("15:04:05"))
			b.WriteString(`</div></div>`)
		case RoleAssistant:
			fmt.Fprintf(&b, `<div class="msg-row msg-row-assistant flex justify-start">`)
			b.WriteString(`<div class="flex flex-col items-start gap-0.5 max-w-[90%]">`)
			fmt.Fprintf(&b, `<span class="text-[10px] uppercase tracking-wider text-zinc-400 font-semibold">agent</span>`)
			fmt.Fprintf(&b, `<div class="msg-bubble msg-bubble-agent px-3 py-2 rounded-2xl rounded-tl-sm bg-zinc-800/80 border border-zinc-700/60 text-zinc-100 text-[13px] leading-relaxed whitespace-pre-wrap break-words">%s</div>`, htmlEscape(m.Content))
			fmt.Fprintf(&b, `<span class="text-[10px] text-zinc-500 font-mono">%s</span>`, m.At.Format("15:04:05"))
			b.WriteString(`</div></div>`)
		case RoleTool:
			args := compactJSON(m.Args)
			// One-liner: tool_name(args)
			fmt.Fprintf(&b, `<div class="msg-row msg-row-tool flex items-center gap-2 px-2 py-1 text-[11px] font-mono text-zinc-500">`)
			b.WriteString(`<span class="text-zinc-600">›</span>`)
			fmt.Fprintf(&b, `<span class="text-amber-400/80">%s</span>`, htmlEscape(m.Tool))
			fmt.Fprintf(&b, `<span class="text-zinc-600">(</span><span class="truncate text-zinc-500">%s</span><span class="text-zinc-600">)</span>`, htmlEscape(args))
			fmt.Fprintf(&b, `<span class="ml-auto text-[10px] text-zinc-600">%s</span>`, m.At.Format("15:04:05"))
			b.WriteString(`</div>`)
		case RoleStatus:
			fmt.Fprintf(&b, `<div class="msg-row msg-row-status flex justify-center">`)
			fmt.Fprintf(&b, `<div class="text-[11px] italic text-zinc-500 px-3 py-1 rounded-full bg-zinc-800/40 border border-zinc-800">%s</div>`, htmlEscape(m.Content))
			b.WriteString(`</div>`)
		case RoleError:
			fmt.Fprintf(&b, `<div class="msg-row msg-row-error flex justify-start">`)
			b.WriteString(`<div class="flex flex-col items-start gap-0.5 max-w-[90%]">`)
			fmt.Fprintf(&b, `<span class="text-[10px] uppercase tracking-wider text-red-400/80 font-semibold">error</span>`)
			fmt.Fprintf(&b, `<div class="px-3 py-2 rounded-2xl rounded-tl-sm bg-red-500/15 border border-red-500/40 text-red-200 text-[13px] leading-snug whitespace-pre-wrap break-words">%s</div>`, htmlEscape(m.Content))
			fmt.Fprintf(&b, `<span class="text-[10px] text-zinc-500 font-mono">%s</span>`, m.At.Format("15:04:05"))
			b.WriteString(`</div></div>`)
		}
	}
	return b.String()
}

// figureSVG returns a small inline SVG glyph keyed by kind. All glyphs use
// currentColor so themes can recolor them via CSS.
func figureSVG(kind string) string {
	switch kind {
	case "rocket":
		return `<svg viewBox="0 0 64 64" xmlns="http://www.w3.org/2000/svg" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round">
<path d="M32 4c10 6 14 16 14 26v14H18V30C18 20 22 10 32 4z"/>
<circle cx="32" cy="24" r="4" fill="currentColor" opacity="0.25"/>
<path d="M18 36l-8 6 4 8 6-4"/>
<path d="M46 36l8 6-4 8-6-4"/>
<path d="M26 50c1 4 3 7 6 9 3-2 5-5 6-9"/>
</svg>`
	case "graph":
		return `<svg viewBox="0 0 64 64" xmlns="http://www.w3.org/2000/svg" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round">
<path d="M8 54h48"/>
<path d="M8 8v46"/>
<path d="M14 44l10-12 8 6 14-20"/>
<circle cx="14" cy="44" r="2.5" fill="currentColor"/>
<circle cx="24" cy="32" r="2.5" fill="currentColor"/>
<circle cx="32" cy="38" r="2.5" fill="currentColor"/>
<circle cx="46" cy="18" r="3" fill="currentColor"/>
</svg>`
	case "team":
		return `<svg viewBox="0 0 64 64" xmlns="http://www.w3.org/2000/svg" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round">
<circle cx="20" cy="22" r="6"/>
<circle cx="44" cy="22" r="6"/>
<circle cx="32" cy="18" r="7" fill="currentColor" opacity="0.18"/>
<path d="M8 50c0-7 6-12 12-12s12 5 12 12"/>
<path d="M32 50c0-7 6-12 12-12s12 5 12 12"/>
</svg>`
	case "globe":
		return `<svg viewBox="0 0 64 64" xmlns="http://www.w3.org/2000/svg" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round">
<circle cx="32" cy="32" r="22"/>
<ellipse cx="32" cy="32" rx="10" ry="22"/>
<path d="M10 32h44"/>
<path d="M14 20c6 4 30 4 36 0"/>
<path d="M14 44c6-4 30-4 36 0"/>
</svg>`
	case "lightbulb":
		return `<svg viewBox="0 0 64 64" xmlns="http://www.w3.org/2000/svg" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round">
<path d="M22 40c-4-3-7-8-7-14a17 17 0 0 1 34 0c0 6-3 11-7 14v6H22z"/>
<path d="M26 50h12"/>
<path d="M28 56h8"/>
<path d="M32 14v-6"/>
<path d="M14 22l-5-3"/>
<path d="M50 22l5-3"/>
</svg>`
	case "gears":
		return `<svg viewBox="0 0 64 64" xmlns="http://www.w3.org/2000/svg" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round">
<circle cx="24" cy="28" r="9"/>
<circle cx="24" cy="28" r="3" fill="currentColor"/>
<path d="M24 14v-4M24 46v-4M10 28h-4M42 28h-4M14 18l-3-3M34 41l-3-3M14 38l-3 3M34 15l-3 3"/>
<circle cx="46" cy="46" r="6"/>
<circle cx="46" cy="46" r="2" fill="currentColor"/>
<path d="M46 38v-2M46 56v-2M38 46h-2M56 46h-2"/>
</svg>`
	}
	return ""
}

func compactJSON(s string) string {
	if s == "" {
		return ""
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return s
	}
	out, err := json.Marshal(v)
	if err != nil {
		return s
	}
	return string(out)
}

// htmlEscape is a tiny replacement for template.HTMLEscapeString — kept inline
// so the file remains import-light.
func htmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		"\"", "&#34;",
		"'", "&#39;",
	)
	return r.Replace(s)
}

// ─── main ─────────────────────────────────────────────────────────────────────

func main() {
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		log.Fatal("OPENAI_API_KEY is required")
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8765"
	}

	app := newApp()
	app.setupAgent(openai.NewProvider(key))

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", app.handleIndex)
	mux.HandleFunc("POST /chat", app.handleChat)
	mux.HandleFunc("GET /sse/deck", app.sseDeck)
	mux.HandleFunc("GET /sse/chat", app.sseChat)

	addr := ":" + port
	log.Printf("Slide builder live at http://localhost%s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}
