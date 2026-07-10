// Example 20: deploy the Looper core as your own supervision server.
//
// Unlike `looper serve` (the shipped CLI), this shows the *production* pattern:
// embed the web.Server in YOUR OWN binary, load config from looper.json, gate it
// behind auth, register an in-process runner, and serve it as a long-lived
// control panel. Your ops team logs in and watches every agent run — including
// runs streamed in from external agents pointed at /ingest.
//
// What it demonstrates:
//   - a login-gated control panel + chat supervision surface for prod;
//   - an ingest bearer token so *external* agents (other processes/services)
//     can post their traces into your panel over the network;
//   - a custom pricing dictionary (looper.json model_costs) so your own
//     gateway/model rates drive cost tracking, not the built-in matrix;
//   - folder persistence by default, PostgreSQL when a DSN is configured;
//   - an in-process "support" agent whose one tool spawns a sub-agent, so the
//     panel renders the parent→child run tree (forward ctx → ParentRunID links).
//
// Run it:
//
//	make ui-build                 # build the real SolidJS UI into the binary
//	go run ./examples/20_server_panel
//	# open http://localhost:9090 and log in (see looper.example.json)
//
// External agents connect by setting, in their own environment:
//
//	LOOPER_TRACE_ENDPOINT=http://<host>:9090/ingest
//	LOOPER_INGEST_TOKEN=<the token this server logs at boot>
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cuatroochenta-idi/looper-agent/internal/config"
	"github.com/cuatroochenta-idi/looper-agent/internal/store/postgres"
	"github.com/cuatroochenta-idi/looper-agent/internal/web"
	"github.com/cuatroochenta-idi/looper-agent/loop"
	"github.com/cuatroochenta-idi/looper-agent/looper"
	"github.com/cuatroochenta-idi/looper-agent/provider"
	"github.com/cuatroochenta-idi/looper-agent/provider/anthropic"
	"github.com/cuatroochenta-idi/looper-agent/provider/google"
	"github.com/cuatroochenta-idi/looper-agent/provider/openai"
	"github.com/cuatroochenta-idi/looper-agent/telemetry"
	"github.com/cuatroochenta-idi/looper-agent/tool"
)

// One shared cost model, so the price map (built-in + any custom overrides) is
// loaded once and reused across every priced run.
var costModel = telemetry.NewCostModel()

func main() {
	// 1. Config: file + env + defaults. "" auto-discovers ./looper.json (then
	//    $LOOPER_CONFIG). In prod you'd point --config at a mounted secret; here
	//    the example ships a looper.example.json to copy to looper.json.
	cfg, err := config.Load("")
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	// 2. Persistence: folder store by default (zero-dependency, great for a
	//    single box), Postgres when cfg.DB is set (multi-replica, durable, the
	//    real prod choice). cfg.DB wins over the folder store.
	var persistOpt web.ServerOption
	storageLabel := "folder " + cfg.StoreDir
	if cfg.DB != "" {
		pg, err := postgres.NewPostgres(context.Background(), cfg.DB)
		if err != nil {
			log.Fatalf("open postgres: %v", err)
		}
		persistOpt = web.WithPersistence(pg)
		storageLabel = "postgres"
	} else {
		persistOpt = web.WithStoreDir(cfg.StoreDir)
	}

	// 3. Custom pricing: your looper.json model_costs override the built-in
	//    matrix during estimation, so cost tracking reflects YOUR negotiated
	//    rates (or a gateway's) instead of list prices.
	if len(cfg.ModelCosts) > 0 {
		costModel.WithCustomCosts(cfg.ModelCosts)
	}

	srv, err := web.NewServer(persistOpt)
	if err != nil {
		log.Fatalf("create server: %v", err)
	}
	srv.SetRunner(buildRunner())

	// 4. Auth: setting auth.password enables a login gate. In prod you ALWAYS
	//    want this — the panel exposes every prompt, output and cost. Nil-safe:
	//    with no password the panel is open and Middleware is a pass-through.
	var auth *web.Auth
	if cfg.AuthEnabled() {
		a := cfg.Auth
		auth = web.NewAuth(a.Username, a.Password, a.SessionSecret, a.IngestToken).
			WithSecureCookies(false) // set true only when served behind HTTPS/TLS
	}

	// Compose a parent mux: the three auth endpoints are registered explicitly
	// (they shadow anything inside srv.Handler() by ServeMux specificity); the
	// wrapped Middleware whitelists them + guards everything else behind the
	// session cookie, and requires the ingest bearer token on /ingest.
	root := http.NewServeMux()
	if auth != nil {
		root.HandleFunc("POST /api/login", auth.LoginHandler)
		root.HandleFunc("POST /api/logout", auth.LogoutHandler)
		root.HandleFunc("GET /api/me", auth.MeHandler)
	}
	root.Handle("/", srv.Handler())
	handler := auth.Middleware(root) // nil auth => root unwrapped

	providerLabel := os.Getenv("LOOPER_PROVIDER")
	if providerLabel == "" {
		providerLabel = "openai"
	}
	addr := fmt.Sprintf(":%d", cfg.Port)
	ingest := fmt.Sprintf("http://localhost:%d/ingest", cfg.Port)
	log.Printf("Control panel : http://localhost%s", addr)
	log.Printf("Trace ingest  : %s", ingest)
	log.Printf("Storage       : %s", storageLabel)
	log.Printf("Provider      : %s", providerLabel)
	if auth != nil {
		// Print the effective ingest token so external agents can be pointed at
		// this panel: they set LOOPER_INGEST_TOKEN to this value.
		log.Printf("Auth          : enabled; ingest bearer token: %s", auth.IngestToken())
		if auth.EphemeralSessionKey() {
			log.Printf("warning: no auth.session_secret set — sessions won't survive a restart")
		}
	}

	// 5. SSE-safe HTTP server: NO WriteTimeout (it would kill long-lived SSE
	//    streams); the web package bounds each SSE write individually instead.
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
		close(serverErr)
	}()

	// 6. Graceful shutdown on SIGINT/SIGTERM (drains in-flight requests).
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	select {
	case err := <-serverErr:
		if err != nil {
			log.Fatalf("server error: %v", err)
		}
	case <-sigs:
		log.Printf("shutting down…")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(ctx)
	}
}

// ─── In-process runner: the "support" agent behind POST /api/run ──────────────

const supportSystemPrompt = "You are a support agent exposed through an internal " +
	"control panel. You have three tools: get_status(service), add(a,b), and " +
	"escalate(question) which hands a hard question to a specialist sub-agent. " +
	"Use escalate for anything requiring deep reasoning; otherwise answer in one " +
	"short sentence."

// buildRunner adapts a looper.Agent to the web.RunFunc contract the panel calls
// on POST /api/run. It streams the agent's steps to the timeline and returns a
// final summary — the same shape `looper serve` uses.
func buildRunner() web.RunFunc {
	return func(ctx context.Context, input string) (<-chan web.StepEvent, <-chan web.RunSummary, error) {
		p, err := buildProvider()
		if err != nil {
			return nil, nil, err
		}

		agent, err := looper.NewAgent(p, supportSystemPrompt,
			statusTool(), addTool(), escalateTool(p))
		if err != nil {
			return nil, nil, fmt.Errorf("build agent: %w", err)
		}

		steps := make(chan web.StepEvent, 32)
		summary := make(chan web.RunSummary, 1)
		go func() {
			defer close(steps)
			defer close(summary)

			iter := agent.Iterate(ctx, input)
			// Tokens ride the first step of each turn; blank them on repeats so
			// the timeline doesn't double-count a turn's usage across its tool
			// calls (the framework shares one Usage pointer per LLM response).
			shown := make(map[int]bool)
			for s := range iter.Next() {
				ws := toWebStep(s)
				if ws.InputTokens > 0 || ws.OutputTokens > 0 {
					if shown[s.Turn] {
						ws.InputTokens, ws.OutputTokens, ws.CachedTokens = 0, 0, 0
					} else {
						shown[s.Turn] = true
					}
				}
				steps <- ws
			}

			res := iter.Result()
			// res.Cost already went through the precision cascade (API-reported
			// cost per call, then pricing tables) with per-(provider,model)
			// attribution — don't recompute from aggregate tokens.
			summary <- web.RunSummary{
				Output:           res.Output,
				Status:           res.Status,
				Turns:            res.Turns,
				TotalUSD:         res.Cost.TotalUSD,
				CostEstimated:    res.Cost.Estimated,
				InputTokens:      res.Usage.InputTokens,
				OutputTokens:     res.Usage.OutputTokens,
				CachedTokens:     res.Usage.CachedTokens,
				CacheWriteTokens: res.Usage.CacheWriteTokens,
			}
		}()

		return steps, summary, nil
	}
}

// ─── Tools ────────────────────────────────────────────────────────────────────

type statusIn struct {
	Service string `json:"service" jsonschema:"description=Service name to check"`
}
type addIn struct {
	A float64 `json:"a" jsonschema:"description=First operand"`
	B float64 `json:"b" jsonschema:"description=Second operand"`
}
type escalateIn struct {
	Question string `json:"question" jsonschema:"description=A hard question to delegate to a specialist"`
}

func statusTool() *tool.Tool {
	return tool.MustNewTool(statusIn{},
		func(_ context.Context, in statusIn) (string, error) {
			return fmt.Sprintf("service %q is healthy (mock): 3 replicas, p99 42ms", in.Service), nil
		},
		tool.ToolConfig{Name: "get_status", Description: "Return a mock health snapshot for a service."},
	)
}

func addTool() *tool.Tool {
	return tool.MustNewTool(addIn{},
		func(_ context.Context, in addIn) (string, error) {
			return fmt.Sprintf("%g", in.A+in.B), nil
		},
		tool.ToolConfig{Name: "add", Description: "Add two numbers.", Parallel: true},
	)
}

// escalateTool is the nesting point: its body spins up a fresh specialist
// sub-agent and runs it to completion, returning its answer as the tool result.
// Forwarding ctx is what links the child run to this run in the panel — the
// sub-agent reads ParentRunID off ctx, so the panel nests it under this call.
func escalateTool(p provider.LLMProvider) *tool.Tool {
	const specialistPrompt = "You are a senior specialist. Answer the question " +
		"precisely in 2-3 sentences. No preamble."
	return tool.MustNewTool(escalateIn{},
		func(ctx context.Context, in escalateIn) (string, error) {
			sub, err := looper.NewAgent(p, specialistPrompt)
			if err != nil {
				return "", fmt.Errorf("build specialist: %w", err)
			}
			res, err := sub.Run(ctx, in.Question) // ctx → ParentRunID linkage
			if err != nil {
				return "", fmt.Errorf("specialist: %w", err)
			}
			return res.Output, nil
		},
		tool.ToolConfig{Name: "escalate", Description: "Delegate a hard question to a specialist sub-agent."},
	)
}

// ─── Provider selection (LOOPER_PROVIDER, like `looper serve`) ────────────────

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

// toWebStep maps a loop.Step onto the panel's StepEvent wire shape.
func toWebStep(s loop.Step) web.StepEvent {
	out := web.StepEvent{
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
	case loop.StepStreamingChunk:
		out.Kind = web.StepKindStreamingChunk
	case loop.StepReasoningChunk:
		out.Kind = web.StepKindReasoning
	}
	return out
}
