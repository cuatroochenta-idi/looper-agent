// Example: minimalist code-dev TUI driven by a local LLM, polished with
// bubbletea / bubbles / lipgloss.
//
// Talks to any OpenAI-compatible endpoint — defaults to LM Studio at
// http://localhost:1234/v1 with the qwen3.6-35b-a3b model. The agent has
// filesystem + shell tools; destructive ones (write_file, edit_file,
// run_command) are gated by an in-app y/n approval modal so you can't
// silently lose work.
//
// Usage
//
//	# In LM Studio: load qwen3.6-35b-a3b and start the local server.
//	go run ./examples/11_dev_cli
//
//	# Talk to OpenAI instead:
//	OPENAI_API_KEY=sk-... go run ./examples/11_dev_cli \
//	    --endpoint https://api.openai.com/v1 --model gpt-4o
//
//	# Trust mode — no approval prompts (use in disposable dirs):
//	go run ./examples/11_dev_cli --no-approve --cwd /tmp/scratch
//
// Keybindings
//
//	Enter         submit prompt
//	Ctrl-J        newline in input (multi-line)
//	Y / N         answer the approval modal
//	Esc           abort the current agent turn
//	Ctrl-L        clear the chat log
//	Ctrl-R        reset conversation history
//	PgUp / PgDn   scroll the log
//	Ctrl-C        quit (also: /quit)
//
// Slash commands inside the input
//
//	/reset   forget conversation history
//	/cwd     print working directory
//	/tools   list available tools
//	/quit    exit
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/cuatroochenta-idi/looper-agent/looper"
	"github.com/cuatroochenta-idi/looper-agent/loop"
	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/pause"
	"github.com/cuatroochenta-idi/looper-agent/provider"
	"github.com/cuatroochenta-idi/looper-agent/provider/openai"
	"github.com/cuatroochenta-idi/looper-agent/tool"
)

// ─── Tool input schemas ───────────────────────────────────────────────────────

type ListDirIn struct {
	Path string `json:"path" jsonschema:"description=Directory relative to cwd"`
}

type ReadFileIn struct {
	Path string `json:"path" jsonschema:"description=File path relative to cwd"`
}

type WriteFileIn struct {
	Path    string `json:"path"`
	Content string `json:"content" jsonschema:"description=Full new file content"`
}

type EditFileIn struct {
	Path    string `json:"path"`
	Find    string `json:"find" jsonschema:"description=Exact text to replace; must occur exactly once"`
	Replace string `json:"replace"`
}

type SearchIn struct {
	Pattern string `json:"pattern" jsonschema:"description=Plain-text substring to look for"`
	Path    string `json:"path" jsonschema:"description=Directory to search; empty = cwd"`
}

type RunIn struct {
	Command string `json:"command" jsonschema:"description=Shell command to run; whole line"`
}

// ─── App state (shared between the TUI model and the agent goroutine) ────────

type app struct {
	cwd    string
	pm     *pause.PauseManager
	paused map[string]bool
}

func (a *app) resolve(rel string) (string, error) {
	if rel == "" {
		rel = "."
	}
	full := filepath.Clean(filepath.Join(a.cwd, rel))
	relCheck, err := filepath.Rel(a.cwd, full)
	if err != nil || strings.HasPrefix(relCheck, "..") {
		return "", fmt.Errorf("path %q escapes the working directory", rel)
	}
	return full, nil
}

func (a *app) tools() []*tool.Tool {
	return []*tool.Tool{
		tool.MustNewTool(ListDirIn{}, a.listDir,
			tool.ToolConfig{Name: "list_dir", Description: "List a directory. Returns one entry per line: 'd|f  size  name'.", Parallel: true}),
		tool.MustNewTool(ReadFileIn{}, a.readFile,
			tool.ToolConfig{Name: "read_file", Description: "Read a UTF-8 text file. First 4 KiB only.", Parallel: true}),
		tool.MustNewTool(SearchIn{}, a.search,
			tool.ToolConfig{Name: "search_code", Description: "Recursive substring search. Up to 30 hits, one per line: 'path:lineno: snippet'.", Parallel: true}),
		tool.MustNewTool(WriteFileIn{}, a.writeFile,
			tool.ToolConfig{Name: "write_file", Description: "Create or overwrite a file. DESTRUCTIVE — requires approval."}),
		tool.MustNewTool(EditFileIn{}, a.editFile,
			tool.ToolConfig{Name: "edit_file", Description: "Find/replace inside a file. 'find' must occur exactly once. DESTRUCTIVE — requires approval."}),
		tool.MustNewTool(RunIn{}, a.runCmd,
			tool.ToolConfig{Name: "run_command", Description: "Run a shell command in cwd. DESTRUCTIVE — requires approval. Captures stdout+stderr, capped at 4 KiB."}),
	}
}

func (a *app) listDir(_ context.Context, in ListDirIn) (string, error) {
	full, err := a.resolve(in.Path)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, e := range entries {
		kind := "f"
		if e.IsDir() {
			kind = "d"
		}
		var size int64
		if info, err := e.Info(); err == nil {
			size = info.Size()
		}
		fmt.Fprintf(&b, "%s  %8d  %s\n", kind, size, e.Name())
	}
	if b.Len() == 0 {
		return "(empty directory)", nil
	}
	return b.String(), nil
}

func (a *app) readFile(_ context.Context, in ReadFileIn) (string, error) {
	full, err := a.resolve(in.Path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	const cap = 4 * 1024
	if len(data) > cap {
		return string(data[:cap]) + fmt.Sprintf("\n…(truncated, file is %d bytes)", len(data)), nil
	}
	return string(data), nil
}

func (a *app) search(_ context.Context, in SearchIn) (string, error) {
	root, err := a.resolve(in.Path)
	if err != nil {
		return "", err
	}
	pat := strings.ToLower(in.Pattern)
	if pat == "" {
		return "", fmt.Errorf("pattern is required")
	}
	var hits []string
	const maxHits = 30
	walker := func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			if err == nil && shouldSkipDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if len(hits) >= maxHits {
			return fs.SkipAll
		}
		if isBinaryExt(filepath.Ext(d.Name())) {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		lineNo := 0
		rel, _ := filepath.Rel(a.cwd, path)
		for sc.Scan() {
			lineNo++
			line := sc.Text()
			if strings.Contains(strings.ToLower(line), pat) {
				snip := strings.TrimSpace(line)
				if len(snip) > 140 {
					snip = snip[:140] + "…"
				}
				hits = append(hits, fmt.Sprintf("%s:%d: %s", rel, lineNo, snip))
				if len(hits) >= maxHits {
					return fs.SkipAll
				}
			}
		}
		return nil
	}
	if err := filepath.WalkDir(root, walker); err != nil && err != fs.SkipAll {
		return "", err
	}
	if len(hits) == 0 {
		return "(no matches)", nil
	}
	return strings.Join(hits, "\n"), nil
}

func (a *app) writeFile(_ context.Context, in WriteFileIn) (string, error) {
	full, err := a.resolve(in.Path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(full, []byte(in.Content), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(in.Content), in.Path), nil
}

func (a *app) editFile(_ context.Context, in EditFileIn) (string, error) {
	full, err := a.resolve(in.Path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	src := string(data)
	occ := strings.Count(src, in.Find)
	if occ == 0 {
		return "", fmt.Errorf("'find' string not found in %s", in.Path)
	}
	if occ > 1 {
		return "", fmt.Errorf("'find' string occurs %d times in %s — make it unique first", occ, in.Path)
	}
	out := strings.Replace(src, in.Find, in.Replace, 1)
	if err := os.WriteFile(full, []byte(out), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("edited %s (%d → %d bytes)", in.Path, len(data), len(out)), nil
}

func (a *app) runCmd(ctx context.Context, in RunIn) (string, error) {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", in.Command)
	cmd.Dir = a.cwd
	out, err := cmd.CombinedOutput()
	const cap = 4 * 1024
	body := string(out)
	if len(body) > cap {
		body = body[:cap] + fmt.Sprintf("\n…(truncated, %d bytes total)", len(out))
	}
	if err != nil {
		return body + "\n[exit error: " + err.Error() + "]", nil
	}
	if body == "" {
		body = "(no output)"
	}
	return body, nil
}

func shouldSkipDir(name string) bool {
	switch name {
	case ".git", ".hg", "node_modules", "vendor", ".idea", ".vscode", "dist", "build", "target", ".looper":
		return true
	}
	return false
}

func isBinaryExt(ext string) bool {
	switch strings.ToLower(ext) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".pdf", ".zip", ".tar", ".gz", ".tgz", ".so", ".dylib", ".exe", ".bin":
		return true
	}
	return false
}

// ─── Lipgloss palette ─────────────────────────────────────────────────────────

var (
	accentColor  = lipgloss.Color("63")  // indigo
	mutedColor   = lipgloss.Color("244") // dim grey
	faintColor   = lipgloss.Color("238")
	bgPanelColor = lipgloss.Color("235")
	bgBarColor   = lipgloss.Color("236")
	successColor = lipgloss.Color("78")
	warningColor = lipgloss.Color("214")
	dangerColor  = lipgloss.Color("204")
	userColor    = lipgloss.Color("117")
	toolColor    = lipgloss.Color("220")

	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("230")).
			Background(accentColor).
			Padding(0, 1)

	subHeaderStyle = lipgloss.NewStyle().
			Foreground(mutedColor).
			Padding(0, 1)

	statusStyle = lipgloss.NewStyle().
			Background(bgBarColor).
			Foreground(lipgloss.Color("250")).
			Padding(0, 1)

	userBubbleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(userColor)

	assistantStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252"))

	toolCallStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(toolColor)

	toolNameStyle = lipgloss.NewStyle().
			Foreground(warningColor).
			Bold(true)

	toolArgsStyle = lipgloss.NewStyle().
			Foreground(mutedColor)

	toolResultStyle = lipgloss.NewStyle().
			Foreground(successColor).
			Faint(true)

	errorStyle = lipgloss.NewStyle().
			Foreground(dangerColor).
			Bold(true)

	dimStyle = lipgloss.NewStyle().Foreground(mutedColor)

	approvalBoxStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(warningColor).
				Foreground(lipgloss.Color("230")).
				Padding(0, 2).
				Bold(true)

	approvalLabel = lipgloss.NewStyle().
			Background(warningColor).
			Foreground(lipgloss.Color("0")).
			Padding(0, 1).
			Bold(true)
)

// ─── TUI model ────────────────────────────────────────────────────────────────

type chatRole int

const (
	roleUser chatRole = iota
	roleAssistant
	roleTool
	roleStatus
	roleError
	roleReasoning
)

// chatMsg is one rendered conversation entry. Pieces are stored separately so
// the view can re-render them with the current width.
type chatMsg struct {
	role     chatRole
	body     string
	toolName string
	toolArgs string
	dur      time.Duration
}

type model struct {
	// Components
	input    textarea.Model
	viewport viewport.Model
	spinner  spinner.Model

	// State
	app      *app
	bridge   *bridge
	agent    *looper.Agent
	history  *message.History
	messages []chatMsg

	// streamBuf is the in-flight assistant text being streamed. It must be a
	// value type (not strings.Builder) because bubbletea passes the model by
	// value through Update — a Builder panics with copyCheck on reuse.
	streamBuf       string
	streamPending   bool
	// reasonBuf accumulates reasoning_chunk deltas for the in-flight turn.
	// Flushed into a single roleReasoning chatMsg when the turn ends or
	// the model switches to visible output.
	reasonBuf       string
	reasonPending   bool
	awaitingApproval *pendingApproval
	agentRunning    bool
	currentCancel   context.CancelFunc

	// Geometry
	width  int
	height int

	// Stats from the last completed turn (header status bar).
	lastStats turnStats

	// Banner config
	model    string
	endpoint string
	noApprove bool
}

type pendingApproval struct {
	tool string
	args string
}

type turnStats struct {
	turns     int
	inTokens  int
	outTokens int
	cost      float64
	dur       time.Duration
}

// ─── Bridge between the agent goroutine and the TUI ──────────────────────────

type bridge struct {
	program    *tea.Program
	approvalCh chan bool
	mu         sync.Mutex
}

// setProgram is called once after tea.NewProgram so the goroutine can Send.
func (b *bridge) setProgram(p *tea.Program) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.program = p
}

func (b *bridge) send(msg tea.Msg) {
	b.mu.Lock()
	p := b.program
	b.mu.Unlock()
	if p != nil {
		p.Send(msg)
	}
}

// ─── Tea messages flowing from agent goroutine into the model ────────────────

type agentStepMsg struct{ step loop.Step }
type agentDoneMsg struct{ stats turnStats }
type agentErrMsg struct{ err error }
type approvalRequestMsg struct{ tool, args string }
type approvalResponseMsg struct{ ok bool }

// ─── Bubbletea wiring ────────────────────────────────────────────────────────

func newModel(a *app, agent *looper.Agent, mdl, endpoint string, noApprove bool) model {
	ti := textarea.New()
	ti.Placeholder = "ask me to refactor, audit, fix bugs, run tests …  (Enter to send · Ctrl-J for newline)"
	ti.Prompt = "❯ "
	ti.CharLimit = 8000
	ti.SetWidth(80)
	ti.SetHeight(3)
	ti.Focus()
	ti.ShowLineNumbers = false
	ti.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ti.FocusedStyle.Prompt = lipgloss.NewStyle().Foreground(accentColor).Bold(true)

	vp := viewport.New(80, 20)
	vp.Style = lipgloss.NewStyle()

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(warningColor)

	return model{
		input:    ti,
		viewport: vp,
		spinner:  sp,
		app:      a,
		agent:    agent,
		history:  message.NewHistory(),
		messages: []chatMsg{{role: roleStatus, body: greeting(mdl, a.cwd, noApprove)}},
		model:    mdl,
		endpoint: endpoint,
		noApprove: noApprove,
	}
}

func greeting(mdl, cwd string, noApprove bool) string {
	parts := []string{
		"connected to " + mdl,
		"cwd: " + cwd,
	}
	if noApprove {
		parts = append(parts, "⚠ no-approve mode — every tool call will run without asking")
	}
	return strings.Join(parts, "  ·  ")
}

func (m model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, m.spinner.Tick)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.layout()
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case agentStepMsg:
		return m.handleStep(msg.step), nil

	case agentDoneMsg:
		m.flushStream()
		m.lastStats = msg.stats
		m.agentRunning = false
		m.currentCancel = nil
		m.input.Focus()
		m.rebuildViewport()
		return m, nil

	case agentErrMsg:
		m.flushStream()
		m.messages = append(m.messages, chatMsg{role: roleError, body: msg.err.Error()})
		m.agentRunning = false
		m.currentCancel = nil
		m.input.Focus()
		m.rebuildViewport()
		return m, nil

	case approvalRequestMsg:
		m.awaitingApproval = &pendingApproval{tool: msg.tool, args: msg.args}
		m.rebuildViewport() // re-render to show the modal
		return m, nil
	}

	// Bubble updates to children that didn't claim the msg.
	{
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		cmds = append(cmds, cmd)
	}
	{
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
}

// handleKey owns the keyboard. Approval modal takes priority when open.
func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.awaitingApproval != nil {
		switch msg.String() {
		case "y", "Y", "enter":
			m.bridge.approvalCh <- true
			m.awaitingApproval = nil
			m.rebuildViewport()
			return m, nil
		case "n", "N", "esc":
			m.bridge.approvalCh <- false
			m.awaitingApproval = nil
			m.rebuildViewport()
			return m, nil
		}
		return m, nil // swallow other keys while modal is open
	}

	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		if m.agentRunning && m.currentCancel != nil {
			m.currentCancel()
			m.messages = append(m.messages, chatMsg{role: roleStatus, body: "(interrupted)"})
			m.rebuildViewport()
		}
		return m, nil
	case "ctrl+l":
		m.messages = m.messages[:0]
		m.rebuildViewport()
		return m, nil
	case "ctrl+r":
		m.history = message.NewHistory()
		m.messages = append(m.messages, chatMsg{role: roleStatus, body: "(history cleared)"})
		m.rebuildViewport()
		return m, nil
	case "pgup":
		m.viewport.LineUp(5)
		return m, nil
	case "pgdown":
		m.viewport.LineDown(5)
		return m, nil
	case "enter":
		if m.agentRunning {
			return m, nil
		}
		v := strings.TrimSpace(m.input.Value())
		if v == "" {
			return m, nil
		}
		// Slash commands handled here so the agent doesn't see them.
		if strings.HasPrefix(v, "/") {
			m = m.handleSlash(v)
			m.input.Reset()
			return m, nil
		}
		m.input.Reset()
		m.input.Blur()
		m = m.startTurn(v)
		return m, m.spinner.Tick
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m model) handleSlash(line string) model {
	switch line {
	case "/quit", "/exit":
		os.Exit(0)
	case "/cwd":
		m.messages = append(m.messages, chatMsg{role: roleStatus, body: "cwd: " + m.app.cwd})
	case "/tools":
		m.messages = append(m.messages, chatMsg{
			role: roleStatus,
			body: "read-only:  list_dir · read_file · search_code\npaused   :  write_file · edit_file · run_command",
		})
	case "/reset":
		m.history = message.NewHistory()
		m.messages = append(m.messages, chatMsg{role: roleStatus, body: "(history cleared)"})
	default:
		m.messages = append(m.messages, chatMsg{role: roleError, body: "unknown command: " + line})
	}
	m.rebuildViewport()
	return m
}

// startTurn kicks off the agent in a goroutine, fanning every step back into
// the model via bridge.send.
func (m model) startTurn(input string) model {
	m.messages = append(m.messages, chatMsg{role: roleUser, body: input})
	m.agentRunning = true
	m.streamBuf = ""
	m.streamPending = false
	ctx, cancel := context.WithCancel(context.Background())
	m.currentCancel = cancel

	br := m.bridge
	a := m.app
	agent := m.agent
	hist := m.history

	go func() {
		start := time.Now()
		iter := agent.Iterate(ctx, input, looper.WithHistory(hist))
		for step := range iter.Next() {
			br.send(agentStepMsg{step: step})
			if step.Type == loop.StepToolCall && a.paused[step.ToolName] {
				br.send(approvalRequestMsg{tool: step.ToolName, args: step.ToolArgs})
				ok := <-br.approvalCh
				if ok {
					_ = a.pm.Resume(&pause.PauseResponse{Action: "ok"})
				} else {
					_ = a.pm.Resume(&pause.PauseResponse{Action: "cancel"})
				}
			}
		}
		res := iter.Result()
		br.send(agentDoneMsg{stats: turnStats{
			turns:     res.Turns,
			inTokens:  res.Usage.InputTokens,
			outTokens: res.Usage.OutputTokens,
			cost:      res.Cost.TotalUSD,
			dur:       time.Since(start),
		}})
	}()

	// We don't store history mutation here; agentDoneMsg comes back with the
	// final history attached via iter.Result. (We'd capture it there if we
	// wanted to persist it across turns — agent.Iterate already mutates the
	// supplied history slice in place via the loop, so we just keep using the
	// same instance.)
	m.rebuildViewport()
	return m
}

// handleStep folds an incoming loop.Step into the chat log.
func (m model) handleStep(s loop.Step) model {
	switch s.Type {
	case loop.StepReasoningChunk:
		// Reasoning may arrive interleaved with visible content (e.g.
		// Anthropic emits both deltas inside the same content_block
		// stream). Buffer it apart so the live assistant bubble keeps
		// the model's final text clean.
		m.reasonBuf += s.Content
		m.reasonPending = true
		m.upsertReasoning()
	case loop.StepStreamingChunk:
		m.flushReason()
		m.streamBuf += s.Content
		m.streamPending = true
		// Live render: replace the last assistant message in flight, or append.
		m.upsertStreaming()
	case loop.StepToolCall:
		m.flushReason()
		m.flushStream()
		m.messages = append(m.messages, chatMsg{
			role:     roleTool,
			toolName: s.ToolName,
			toolArgs: s.ToolArgs,
		})
	case loop.StepToolResult:
		// Patch the last matching tool entry with its preview result.
		body := strings.TrimSpace(strings.ReplaceAll(s.Content, "\n", " "))
		if len(body) > 140 {
			body = body[:140] + "…"
		}
		for i := len(m.messages) - 1; i >= 0; i-- {
			if m.messages[i].role == roleTool && m.messages[i].toolName == s.ToolName && m.messages[i].body == "" {
				m.messages[i].body = body
				break
			}
		}
	case loop.StepFinalResponse:
		m.flushReason()
		// Streaming chunks already appended everything; the assistant message
		// is set. We only need to consolidate a non-streaming final.
		if !m.streamPending && strings.TrimSpace(s.Content) != "" {
			m.messages = append(m.messages, chatMsg{role: roleAssistant, body: s.Content})
		}
		m.flushStream()
	case loop.StepError:
		m.flushReason()
		m.flushStream()
		if s.Error != nil {
			m.messages = append(m.messages, chatMsg{role: roleError, body: s.Error.Error()})
		}
	}
	m.rebuildViewport()
	return m
}

// upsertStreaming makes sure the latest assistant message contains the current
// stream buffer. Called on every chunk.
func (m *model) upsertStreaming() {
	body := m.streamBuf
	if len(m.messages) > 0 && m.messages[len(m.messages)-1].role == roleAssistant {
		m.messages[len(m.messages)-1].body = body
		return
	}
	m.messages = append(m.messages, chatMsg{role: roleAssistant, body: body})
}

// flushStream resets the streaming buffer so the next chunk will start a fresh
// message if any.
func (m *model) flushStream() {
	m.streamBuf = ""
	m.streamPending = false
}

// upsertReasoning mirrors upsertStreaming for thinking traces — keeps the
// in-flight reasoning bubble updated as deltas arrive.
func (m *model) upsertReasoning() {
	body := m.reasonBuf
	if len(m.messages) > 0 && m.messages[len(m.messages)-1].role == roleReasoning {
		m.messages[len(m.messages)-1].body = body
		return
	}
	m.messages = append(m.messages, chatMsg{role: roleReasoning, body: body})
}

// flushReason resets the reasoning buffer; the rendered bubble stays in
// history.
func (m *model) flushReason() {
	m.reasonBuf = ""
	m.reasonPending = false
}

// ─── Layout & view ────────────────────────────────────────────────────────────

func (m *model) layout() {
	if m.width == 0 || m.height == 0 {
		return
	}
	inputH := m.input.Height() + 2
	statusH := 1
	headerH := 2
	vpH := m.height - inputH - statusH - headerH
	if vpH < 3 {
		vpH = 3
	}
	m.viewport.Width = m.width
	m.viewport.Height = vpH
	m.input.SetWidth(m.width - 4)
	m.rebuildViewport()
}

// rebuildViewport renders the entire chat log + any active modal into the
// viewport's content. It auto-scrolls to the bottom unless the user has
// manually scrolled up (we keep it simple — always scroll to bottom).
func (m *model) rebuildViewport() {
	w := m.viewport.Width
	if w == 0 {
		w = 80
	}
	var b strings.Builder
	for i, msg := range m.messages {
		b.WriteString(renderMsg(msg, w))
		if i < len(m.messages)-1 {
			b.WriteString("\n")
		}
	}
	if m.awaitingApproval != nil {
		b.WriteString("\n")
		b.WriteString(renderApproval(*m.awaitingApproval, w))
	}
	m.viewport.SetContent(b.String())
	m.viewport.GotoBottom()
}

func renderMsg(msg chatMsg, w int) string {
	switch msg.role {
	case roleUser:
		label := userBubbleStyle.Render("❯ you")
		body := wrap(msg.body, w-2)
		return label + "\n  " + body
	case roleAssistant:
		label := lipgloss.NewStyle().Foreground(accentColor).Bold(true).Render("◆ agent")
		body := assistantStyle.Render(wrap(msg.body, w-2))
		return label + "\n  " + body
	case roleTool:
		head := toolCallStyle.Render("⚙ ") + toolNameStyle.Render(msg.toolName)
		args := toolArgsStyle.Render(truncate(compact(msg.toolArgs), w-len(msg.toolName)-8))
		head += " " + args
		if msg.body == "" {
			head += dimStyle.Render(" · executing…")
		}
		out := head
		if msg.body != "" {
			out += "\n" + lipgloss.NewStyle().Foreground(successColor).Render("  └─ ") +
				toolResultStyle.Render(wrap(msg.body, w-4))
		}
		return out
	case roleStatus:
		return dimStyle.Render("· " + msg.body)
	case roleError:
		return errorStyle.Render("! ") + lipgloss.NewStyle().Foreground(dangerColor).Render(msg.body)
	case roleReasoning:
		label := dimStyle.Italic(true).Render("✻ thinking")
		body := dimStyle.Italic(true).Render(wrap(msg.body, w-2))
		return label + "\n  " + body
	}
	return msg.body
}

func renderApproval(p pendingApproval, w int) string {
	label := approvalLabel.Render(" APPROVE ")
	tool := toolNameStyle.Render(p.tool)
	args := dimStyle.Render(truncate(compact(p.args), w-len(p.tool)-25))
	body := label + " " + tool + " " + args + "\n" +
		dimStyle.Render("  press ") + lipgloss.NewStyle().Foreground(successColor).Bold(true).Render("y") +
		dimStyle.Render(" to allow · ") + lipgloss.NewStyle().Foreground(dangerColor).Bold(true).Render("n") +
		dimStyle.Render(" to reject")
	return approvalBoxStyle.Render(body)
}

func (m model) View() string {
	if m.width == 0 {
		return "" // pre-init, wait for window size msg
	}

	header := headerStyle.Render(" looper · dev cli ") +
		subHeaderStyle.Render(m.model+"  ·  "+truncate(m.app.cwd, m.width-len(m.model)-30))

	var status string
	switch {
	case m.awaitingApproval != nil:
		status = statusStyle.Render(" awaiting approval · y/n · esc to cancel ")
	case m.agentRunning:
		status = statusStyle.Render(m.spinner.View() + " thinking" +
			fmt.Sprintf("  ·  esc aborts  ·  last: %d in / %d out · $%.5f",
				m.lastStats.inTokens, m.lastStats.outTokens, m.lastStats.cost))
	default:
		shortcuts := "enter send · ctrl+j newline · ctrl+l clear · ctrl+r reset · esc abort · ctrl+c quit"
		if m.lastStats.turns > 0 {
			status = statusStyle.Render(fmt.Sprintf(
				" idle  ·  last: %d turns · %d in / %d out · $%.5f · %s  ·  %s",
				m.lastStats.turns, m.lastStats.inTokens, m.lastStats.outTokens,
				m.lastStats.cost, prettyDur(m.lastStats.dur), shortcuts))
		} else {
			status = statusStyle.Render(" ready  ·  " + shortcuts)
		}
	}

	return lipgloss.JoinVertical(lipgloss.Left,
		header,
		m.viewport.View(),
		m.input.View(),
		status,
	)
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func compact(s string) string {
	s = strings.TrimSpace(s)
	return strings.Join(strings.Fields(s), " ")
}

func truncate(s string, n int) string {
	if n <= 1 || len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func wrap(s string, w int) string {
	if w < 20 {
		w = 20
	}
	return lipgloss.NewStyle().Width(w).Render(s)
}

func prettyDur(d time.Duration) string {
	switch {
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

// ─── main ─────────────────────────────────────────────────────────────────────

func main() {
	endpoint := flag.String("endpoint", "http://localhost:1234/v1", "OpenAI-compatible base URL (LM Studio default)")
	mdl := flag.String("model", "gemma-4-26b-a4b-it", "Model id to use")
	cwdFlag := flag.String("cwd", ".", "Working directory — tool ops scoped here")
	noApprove := flag.Bool("no-approve", false, "Skip approval prompts (dangerous — use disposable dirs)")
	apiKey := flag.String("api-key", "", "API key (only needed for hosted providers)")
	effort := flag.String("reasoning", "", "Reasoning effort: low|medium|high (empty=off; only meaningful on reasoning models)")
	showThinking := flag.Bool("show-thinking", false, "Surface the model's thinking trace in the TUI as a faint bubble")
	flag.Parse()

	cwd, err := filepath.Abs(*cwdFlag)
	if err != nil {
		log.Fatal(err)
	}
	if info, err := os.Stat(cwd); err != nil || !info.IsDir() {
		log.Fatalf("cwd %q is not a directory", cwd)
	}

	key := *apiKey
	if key == "" {
		key = os.Getenv("OPENAI_API_KEY")
	}
	if key == "" {
		key = "local-no-key" // LM Studio doesn't validate the key
	}

	pOpts := []openai.Option{
		openai.WithBaseURL(*endpoint),
		openai.WithModel(*mdl),
	}
	// Wire reasoning at the provider so it applies to every request.
	// Both flags are independent: --reasoning sets the effort the
	// remote model uses; --show-thinking decides whether the trace
	// reaches the TUI.
	if *effort != "" {
		pOpts = append(pOpts, openai.WithReasoningEffort(provider.ReasoningEffort(*effort)))
	}
	if *showThinking {
		pOpts = append(pOpts, openai.WithIncludeReasoning(true))
	}
	p := openai.NewProvider(key, pOpts...)

	pm := pause.NewPauseManager()
	pausedTools := map[string]bool{
		"write_file":  true,
		"edit_file":   true,
		"run_command": true,
	}
	if !*noApprove {
		for name := range pausedTools {
			pm.SetPausePoint(name, pause.PauseToolConfirm, 10*time.Minute)
		}
	}

	a := &app{cwd: cwd, pm: pm, paused: pausedTools}

	toolList := a.tools()
	components := make([]any, 0, len(toolList)+2)
	for _, t := range toolList {
		components = append(components, t)
	}
	components = append(components,
		looper.WithAgentPause(pm),
		looper.WithAgentMaxTurns(40),
	)
	agent := looper.MustNewAgent(p, sysPrompt, components...)

	br := &bridge{approvalCh: make(chan bool, 1)}
	mdlState := newModel(a, agent, *mdl, *endpoint, *noApprove)
	mdlState.bridge = br

	prog := tea.NewProgram(mdlState, tea.WithAltScreen(), tea.WithMouseCellMotion())
	br.setProgram(prog)

	if _, err := prog.Run(); err != nil {
		log.Fatal(err)
	}
}

const sysPrompt = `You are a terminal-resident developer assistant.

You have filesystem and shell tools — use them proactively:
  - list_dir, read_file, search_code: discover the project before suggesting changes.
  - write_file, edit_file, run_command: apply changes.

Conventions:
  - Paths are relative to the user's working directory. Never invent paths — list_dir first.
  - Before any destructive operation, briefly explain what you're about to do and why.
  - Every write_file/edit_file/run_command is gated by a human approval, so propose freely;
    if the user rejects, pick a different approach without retrying the same call.
  - Prefer edit_file (find/replace) over write_file when changing a few lines.
  - Keep replies tight. The user reads everything in a terminal — bullets, short sentences, code blocks
    only when actually showing code.`
