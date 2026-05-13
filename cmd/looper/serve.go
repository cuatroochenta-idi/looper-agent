package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/cuatroochenta-idi/looper-agent/looper"
	"github.com/cuatroochenta-idi/looper-agent/internal/web"
	"github.com/cuatroochenta-idi/looper-agent/loop"
	"github.com/cuatroochenta-idi/looper-agent/provider"
	"github.com/cuatroochenta-idi/looper-agent/provider/anthropic"
	"github.com/cuatroochenta-idi/looper-agent/provider/google"
	"github.com/cuatroochenta-idi/looper-agent/provider/openai"
	"github.com/cuatroochenta-idi/looper-agent/telemetry"
	"github.com/cuatroochenta-idi/looper-agent/tool"
)

// costModel computes USD figures from token usage. Shared across runs so the
// price map is loaded once.
var costModel = telemetry.NewCostModel()

// serveCmd boots the debug panel and, optionally, runs a child program with
// LOOPER_TRACE_ENDPOINT pointing at the panel's ingest endpoint. Pattern:
//
//	looper serve [--port N] [--store DIR] [-- <command> [args...]]
//
// Anything after `--` becomes the child command. The child inherits stdio
// and dies when the panel terminates (and vice versa).
func serveCmd(args []string) {
	// Split flags from wrapped command args at the first `--`.
	flagArgs, childArgs := splitDoubleDash(args)

	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	port := fs.Int("port", 9090, "Web UI port")
	storeDir := fs.String("store", ".looper", "Trace store directory (created if missing)")
	fs.Parse(flagArgs)

	srv, err := web.NewServer(web.WithStoreDir(*storeDir))
	if err != nil {
		log.Fatalf("Failed to create web server: %v", err)
	}
	srv.SetRunner(buildRunner())

	addr := fmt.Sprintf(":%d", *port)
	ingest := fmt.Sprintf("http://localhost:%d/ingest", *port)

	log.Printf("Looper Agent UI : http://localhost%s", addr)
	log.Printf("Trace ingest    : %s", ingest)
	log.Printf("Store directory : %s", *storeDir)
	log.Printf("Provider        : %s", providerLabel())

	httpServer := &http.Server{Addr: addr, Handler: srv.Handler()}

	// Start the server. If it stops on its own (port conflict, etc.) we still
	// want to surface it.
	serverDone := make(chan error, 1)
	go func() {
		err := httpServer.ListenAndServe()
		if err != nil && err != http.ErrServerClosed {
			serverDone <- err
		}
		close(serverDone)
	}()

	// SIGINT/SIGTERM gracefully shut down both the server and the child.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)

	if len(childArgs) == 0 {
		// No wrapped command: block until the server stops.
		select {
		case err := <-serverDone:
			if err != nil {
				log.Fatalf("Server error: %v", err)
			}
		case <-sigs:
			shutdown(httpServer)
		}
		return
	}

	// Wrapped command path: wait briefly for the server to come up so the
	// child's first POST doesn't race the listener, then exec.
	time.Sleep(150 * time.Millisecond)

	sessionID := uuid.New().String()
	log.Printf("Session id      : %s", sessionID)
	log.Printf("Launching child : %v", childArgs)

	cmd := exec.Command(childArgs[0], childArgs[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.Env = append(os.Environ(),
		"LOOPER_TRACE_ENDPOINT="+ingest,
		"LOOPER_SESSION_ID="+sessionID,
		"LOOPER_DEBUG=true",
	)
	if err := cmd.Start(); err != nil {
		shutdown(httpServer)
		log.Fatalf("Failed to start child: %v", err)
	}

	childDone := make(chan error, 1)
	go func() { childDone <- cmd.Wait() }()

	select {
	case <-childDone:
		log.Printf("Child exited. Panel kept alive — Ctrl-C to stop.")
		select {
		case <-sigs:
			shutdown(httpServer)
		case <-serverDone:
		}
	case <-sigs:
		log.Printf("Interrupted — terminating child %d", cmd.Process.Pid)
		_ = cmd.Process.Signal(syscall.SIGTERM)
		<-childDone
		shutdown(httpServer)
	case err := <-serverDone:
		if err != nil {
			_ = cmd.Process.Kill()
			log.Fatalf("Server error: %v", err)
		}
	}
}

// splitDoubleDash partitions a flag-style slice at the first `--` token.
// Returns the flags before it and the command-style args after it.
func splitDoubleDash(args []string) (before, after []string) {
	for i, a := range args {
		if a == "--" {
			return args[:i], args[i+1:]
		}
	}
	return args, nil
}

// shutdown gives the server 3 seconds to drain.
func shutdown(s *http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = s.Shutdown(ctx)
}

// ─── Demo tools so the panel has something to call ────────────────────────────

type clockInput struct {
	Timezone string `json:"timezone" jsonschema:"description=IANA timezone like Europe/Madrid"`
}

type addInput struct {
	A float64 `json:"a" jsonschema:"description=First operand"`
	B float64 `json:"b" jsonschema:"description=Second operand"`
}

type echoInput struct {
	Text string `json:"text" jsonschema:"description=Text to echo back"`
}

func demoTools() []*tool.Tool {
	return []*tool.Tool{
		tool.MustNewTool(clockInput{},
			func(_ context.Context, in clockInput) (string, error) {
				tz := in.Timezone
				if tz == "" {
					tz = "UTC"
				}
				return fmt.Sprintf("Pretend now() in %s is 2026-05-12 12:34:56", tz), nil
			},
			tool.ToolConfig{
				Name:        "get_clock",
				Description: "Return a mock current time for a given IANA timezone.",
			},
		),
		tool.MustNewTool(addInput{},
			func(_ context.Context, in addInput) (string, error) {
				return fmt.Sprintf("%g", in.A+in.B), nil
			},
			tool.ToolConfig{
				Name:        "add",
				Description: "Add two numbers and return the sum.",
				Parallel:    true,
			},
		),
		tool.MustNewTool(echoInput{},
			func(_ context.Context, in echoInput) (string, error) {
				return in.Text, nil
			},
			tool.ToolConfig{
				Name:        "echo",
				Description: "Echo the text back unchanged.",
			},
		),
	}
}

// ─── Provider selection ───────────────────────────────────────────────────────

func providerLabel() string {
	switch os.Getenv("LOOPER_PROVIDER") {
	case "anthropic":
		return "anthropic"
	case "google":
		return "google"
	default:
		return "openai"
	}
}

func buildProvider() (provider.LLMProvider, error) {
	switch os.Getenv("LOOPER_PROVIDER") {
	case "anthropic":
		key := os.Getenv("ANTHROPIC_API_KEY")
		if key == "" {
			return nil, fmt.Errorf("ANTHROPIC_API_KEY is empty")
		}
		return anthropic.NewProvider(key), nil
	case "google":
		key := os.Getenv("GOOGLE_API_KEY")
		if key == "" {
			key = os.Getenv("GEMINI_API_KEY")
		}
		if key == "" {
			return nil, fmt.Errorf("GOOGLE_API_KEY (or GEMINI_API_KEY) is empty")
		}
		return google.NewProvider(key), nil
	default:
		key := os.Getenv("OPENAI_API_KEY")
		if key == "" {
			return nil, fmt.Errorf("OPENAI_API_KEY is empty")
		}
		return openai.NewProvider(key), nil
	}
}

// ─── Agent runner adapter ─────────────────────────────────────────────────────

func buildRunner() web.RunFunc {
	systemPrompt := "You are a debug assistant exposed through the Looper Agent " +
		"panel. You have three demo tools: get_clock(timezone), add(a, b), " +
		"and echo(text). Use them whenever the user asks something a tool " +
		"can handle. Otherwise answer in one short sentence."

	return func(ctx context.Context, input string) (<-chan web.StepEvent, <-chan web.RunSummary, error) {
		p, err := buildProvider()
		if err != nil {
			return nil, nil, err
		}

		tools := demoTools()
		// NewAgent takes components as variadic any.
		components := make([]any, 0, len(tools))
		for _, t := range tools {
			components = append(components, t)
		}
		agent, err := looper.NewAgent(p, systemPrompt, components...)
		if err != nil {
			return nil, nil, fmt.Errorf("build agent: %w", err)
		}

		steps := make(chan web.StepEvent, 32)
		summary := make(chan web.RunSummary, 1)

		providerName := providerLabel()
		modelName := p.Model()

		go func() {
			defer close(steps)
			defer close(summary)

			iter := agent.Iterate(ctx, input)
			// Tokens are surfaced once per turn (the first step that carries them).
			// The framework attaches the same Usage pointer to every tool_call
			// emitted from a single LLM response — without this dedupe the
			// timeline would repeat the same in/out numbers on each row.
			tokensShown := make(map[int]bool)
			for step := range iter.Next() {
				ws := toWebStep(step)
				if ws.InputTokens > 0 || ws.OutputTokens > 0 {
					if tokensShown[step.Turn] {
						ws.InputTokens = 0
						ws.OutputTokens = 0
						ws.CachedTokens = 0
					} else {
						tokensShown[step.Turn] = true
					}
				}
				steps <- ws
			}

			res := iter.Result()
			breakdown := costModel.Calculate(providerName, modelName, telemetry.Usage{
				InputTokens:  res.Usage.InputTokens,
				OutputTokens: res.Usage.OutputTokens,
				CachedTokens: res.Usage.CachedTokens,
			})

			summary <- web.RunSummary{
				Output:       res.Output,
				Status:       res.Status,
				Turns:        res.Turns,
				TotalUSD:     breakdown.TotalUSD,
				InputTokens:  res.Usage.InputTokens,
				OutputTokens: res.Usage.OutputTokens,
				CachedTokens: res.Usage.CachedTokens,
			}
		}()

		return steps, summary, nil
	}
}

func toWebStep(s loop.Step) web.StepEvent {
	out := web.StepEvent{
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
	switch s.Type {
	case loop.StepLLMCall:
		out.Kind = web.StepKindLLMCall
	case loop.StepToolCall:
		out.Kind = web.StepKindToolCall
	case loop.StepToolResult:
		out.Kind = web.StepKindToolResult
	case loop.StepFinalResponse:
		out.Kind = web.StepKindFinal
	case loop.StepError:
		out.Kind = web.StepKindError
	case loop.StepSystemPrompt:
		out.Kind = web.StepKindSystemPrompt
	}
	return out
}
