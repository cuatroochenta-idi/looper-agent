// Example: human-in-the-loop pause & resume.
//
// The agent is given a destructive tool (`delete_file`) and the framework's
// pause manager is configured to halt the loop before each invocation,
// waiting for an external decision (here: stdin). The user can:
//
//	ok      → approve, the tool runs
//	cancel  → the tool call is rejected; the model sees an error result
//	         and decides what to do next
//
// Run:
//
//	export OPENAI_API_KEY=sk-...
//	go run ./examples/09_pause_resume
//
// Bonus demo near the bottom (--auto): a side goroutine auto-approves the
// first pause and auto-cancels the second, showing programmatic resume.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/cuatroochenta-idi/looper-agent/looper"
	"github.com/cuatroochenta-idi/looper-agent/loop"
	"github.com/cuatroochenta-idi/looper-agent/pause"
	"github.com/cuatroochenta-idi/looper-agent/provider/openai"
	"github.com/cuatroochenta-idi/looper-agent/tool"
)

// ─── Tool inputs ──────────────────────────────────────────────────────────────

type DeleteFileIn struct {
	Path   string `json:"path" jsonschema:"description=Absolute or relative path to delete"`
	Reason string `json:"reason" jsonschema:"description=Why the file should be deleted"`
}

type ListDirIn struct {
	Path string `json:"path" jsonschema:"description=Directory to list"`
}

func main() {
	autoMode := flag.Bool("auto", false, "Skip stdin and use a programmatic approver (demo)")
	flag.Parse()

	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		log.Fatal("OPENAI_API_KEY required")
	}

	// ── Pause manager ─────────────────────────────────────────────────────────
	// One pause point per "dangerous" tool. The 60 s timeout means an unattended
	// run eventually cancels itself rather than blocking forever.
	pm := pause.NewPauseManager()
	pm.SetPausePoint("delete_file", pause.PauseToolConfirm, 60*time.Second)

	// ── Tools ─────────────────────────────────────────────────────────────────
	deleteFile := tool.MustNewTool(DeleteFileIn{},
		func(_ context.Context, in DeleteFileIn) (string, error) {
			// In a real app this would actually delete; for the demo we just
			// pretend so the user can run it safely.
			return fmt.Sprintf("(pretend) removed %s — reason: %s", in.Path, in.Reason), nil
		},
		tool.ToolConfig{
			Name:        "delete_file",
			Description: "Permanently delete a file from disk. DESTRUCTIVE — every call is gated by a human approval.",
		},
	)
	listDir := tool.MustNewTool(ListDirIn{},
		func(_ context.Context, in ListDirIn) (string, error) {
			// Synthetic directory listing — keeps the demo deterministic.
			files := []string{
				"README.md  4.2K",
				"main.go    1.8K",
				"tmp.log    981K  (45 days old)",
				"build.zip  12M   (2 days old)",
				"secret.env 1.0K",
			}
			return fmt.Sprintf("Contents of %s:\n  %s", in.Path, strings.Join(files, "\n  ")), nil
		},
		tool.ToolConfig{Name: "list_dir", Description: "List the files in a directory."},
	)

	// ── Agent ─────────────────────────────────────────────────────────────────
	agent := looper.MustNewAgent(
		openai.NewProvider(key),
		`You are a careful filesystem assistant. Before deleting anything, list the directory first with list_dir to understand the contents. Always provide a "reason" when calling delete_file. The runtime gates every delete call behind a human approver — when one is rejected, do NOT retry it; pick a different target or stop.`,
		deleteFile, listDir,
		looper.WithAgentPause(pm),
	)

	// ── Approver: stdin (interactive) or programmatic (auto mode) ─────────────
	approver := newStdinApprover(pm)
	if *autoMode {
		approver = newAutoApprover(pm)
	}
	go approver()

	// ── Run a turn that will trigger at least one pause point ─────────────────
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	prompt := "Clean up old files from the current directory. List first, then propose two deletions and execute them."
	fmt.Printf("USER ► %s\n\n", prompt)

	iter := agent.Iterate(ctx, prompt)
	for s := range iter.Next() {
		switch s.Type {
		case loop.StepLLMCall:
			fmt.Printf("  ◆ thinking…  (turn %d)\n", s.Turn)
		case loop.StepToolCall:
			fmt.Printf("  → tool: %s(%s)\n", s.ToolName, s.ToolArgs)
		case loop.StepToolResult:
			fmt.Printf("  ← %s: %s\n", s.ToolName, truncate(s.Content, 90))
		case loop.StepFinalResponse:
			fmt.Printf("\nAGENT ► %s\n", s.Content)
		case loop.StepError:
			fmt.Printf("  ! error: %v\n", s.Error)
		}
	}

	res := iter.Result()
	fmt.Printf("\n— done — status=%s · turns=%d · cost=$%.5f · in=%d · out=%d\n",
		res.Status, res.Turns, res.Cost.TotalUSD, res.Usage.InputTokens, res.Usage.OutputTokens)
}

// ─── Approvers ────────────────────────────────────────────────────────────────
//
// An approver reads pause requests off the manager's response channel and
// pushes an ok/cancel decision back. Real apps would do this over HTTP,
// Slack, SSE, a queue, etc. Here it's just stdin or a scripted goroutine.

// newStdinApprover prompts the operator at every pause point.
func newStdinApprover(pm *pause.PauseManager) func() {
	return func() {
		r := bufio.NewReader(os.Stdin)
		for {
			// Polling pattern: the framework's PauseManager exposes a
			// channel-based response, so the approver waits until a Pause()
			// call is in flight (we infer that from the stdin prompt firing
			// during the agent's tool-call decision).
			//
			// In a real app you'd wire this directly to the pause manager's
			// internal request channel; for the demo we just present the
			// agent's tool name when we see it in the iterator above.
			time.Sleep(200 * time.Millisecond)
			fmt.Print("APPROVE pending delete? [ok/cancel/quit] » ")
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			switch strings.TrimSpace(strings.ToLower(line)) {
			case "ok", "y", "yes":
				_ = pm.Resume(&pause.PauseResponse{Action: "ok"})
			case "cancel", "n", "no":
				_ = pm.Resume(&pause.PauseResponse{Action: "cancel"})
			case "quit", "q":
				return
			default:
				fmt.Println("  (type ok or cancel)")
			}
		}
	}
}

// newAutoApprover scripts ok → cancel → cancel → … so the demo runs
// non-interactively. Approves the first call and rejects the rest.
func newAutoApprover(pm *pause.PauseManager) func() {
	return func() {
		decisions := []string{"ok", "cancel", "cancel", "cancel"}
		for i, d := range decisions {
			time.Sleep(time.Second)
			fmt.Printf("[auto] decision #%d: %s\n", i+1, d)
			_ = pm.Resume(&pause.PauseResponse{Action: d})
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
