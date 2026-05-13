// Example: nested agents — a parent orchestrator that spawns sub-agents
// from inside a tool call, running multiple researchers in parallel.
//
// Why this is interesting
//
//   - Demonstrates that the looper tracer is per-call: every agent.Run /
//     agent.Iterate reads LOOPER_TRACE_ENDPOINT + LOOPER_SESSION_ID from
//     the environment at start time. The parent and the children share
//     the same env vars, so the panel groups all three runs under one
//     session in the sidebar.
//   - Shows the simplest "fan-out" pattern: the parent declares a tool
//     whose body builds and runs a brand new sub-agent. The tool returns
//     the sub-agent's final output as the tool result, which the parent
//     LLM then composes with other sub-agent outputs in its final reply.
//   - Uses Parallel=true so the parent's two research_topic calls fan out
//     concurrently — each sub-agent runs on its own goroutine and posts
//     traces independently. The HTTP trace writer is thread-safe.
//
// How to run
//
//   # CLI only (prints orchestrator + sub-agent progress to stdout):
//   set -a && source .env.local && set +a
//   go run ./examples/10_nested_agents
//
//   # Under the panel (groups all 3 runs under one session in the UI):
//   make build
//   ./bin/looper serve --port 9090 -- go run ./examples/10_nested_agents
//
// Note: no framework changes were needed. Each agent.Run creates its own
// runID; LOOPER_SESSION_ID is shared via process env so the panel groups
// them. The tracer worker is per-Iterate, so concurrent sub-agents each
// open their own short-lived HTTP queue without contention.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"

	"github.com/cuatroochenta-idi/looper-agent/looper"
	"github.com/cuatroochenta-idi/looper-agent/provider/openai"
	"github.com/cuatroochenta-idi/looper-agent/tool"
)

// ─── Sub-agent tools (mock research toolkit) ──────────────────────────────────

type WebLookupIn struct {
	Query string `json:"query" jsonschema:"description=Search query string"`
}

type SummarizeIn struct {
	Text string `json:"text" jsonschema:"description=Long text to shorten"`
}

type AssessCredibilityIn struct {
	Source string `json:"source" jsonschema:"description=Source name or URL"`
}

// canned "search hits" so the demo is self-contained — no network, no key
// for any external service.
var cannedHits = map[string]string{
	"quantum computing": "Source: nature.com — Quantum computers exploit superposition and " +
		"entanglement to evaluate many computational branches at once. As of 2026, IBM, Google " +
		"and IonQ have demonstrated machines with 1000+ physical qubits, but error-correction " +
		"overhead means ~100 logical qubits is the current practical ceiling for production runs.",
	"homomorphic encryption": "Source: eprint.iacr.org — Homomorphic encryption (HE) allows " +
		"computation on ciphertexts without decryption. Modern schemes like CKKS and BFV are " +
		"practical for narrow workloads (ML inference, private analytics) but suffer ~10^4× " +
		"slowdowns vs plaintext. Standardization (ISO/IEC 18033-8) was finalized in 2025.",
}

func newResearcherTools() []*tool.Tool {
	webLookup := tool.MustNewTool(WebLookupIn{},
		func(_ context.Context, in WebLookupIn) (string, error) {
			q := strings.ToLower(strings.TrimSpace(in.Query))
			for k, v := range cannedHits {
				if strings.Contains(q, k) {
					return v, nil
				}
			}
			return fmt.Sprintf("No high-signal hits for %q. Try a more specific query.", in.Query), nil
		},
		tool.ToolConfig{
			Name:        "web_lookup",
			Description: "Search the web for a topic. Returns a short factual snippet with a source attribution.",
			Parallel:    true,
		},
	)

	summarize := tool.MustNewTool(SummarizeIn{},
		func(_ context.Context, in SummarizeIn) (string, error) {
			t := strings.TrimSpace(in.Text)
			if len(t) <= 200 {
				return t, nil
			}
			// Cut at the nearest sentence boundary near 200 chars.
			cut := 200
			if dot := strings.Index(t[cut:], "."); dot != -1 && dot < 80 {
				cut += dot + 1
			}
			return strings.TrimSpace(t[:cut]) + " …", nil
		},
		tool.ToolConfig{
			Name:        "summarize",
			Description: "Shorten a long passage of text to ~200 characters. Use after web_lookup before reporting.",
		},
	)

	assess := tool.MustNewTool(AssessCredibilityIn{},
		func(_ context.Context, in AssessCredibilityIn) (string, error) {
			s := strings.ToLower(in.Source)
			switch {
			case strings.Contains(s, "nature.com"),
				strings.Contains(s, "iacr.org"),
				strings.Contains(s, "ieee.org"):
				return "credibility=HIGH (peer-reviewed venue)", nil
			case strings.Contains(s, "arxiv"):
				return "credibility=MEDIUM (preprint, not peer-reviewed)", nil
			case strings.Contains(s, "blog"), strings.Contains(s, "medium"):
				return "credibility=LOW (informal source)", nil
			}
			return "credibility=UNKNOWN (no signal on this source)", nil
		},
		tool.ToolConfig{
			Name:        "assess_credibility",
			Description: "Tag a source with a credibility label. Use after web_lookup to grade its source.",
			Parallel:    true,
		},
	)

	return []*tool.Tool{webLookup, summarize, assess}
}

const researcherSystemPrompt = `You are a specialised research sub-agent.

You ALWAYS work via your tools — never reply with prose until you have actually
called them:

  1. call web_lookup with a focused query about the topic and focus.
  2. call assess_credibility on the source you just got back.
  3. call summarize on the snippet to shorten it.
  4. ONLY THEN, reply with a single, dense paragraph (~3 sentences) that
     blends the summary with the credibility tag. No preamble, no bullets,
     no "I found that…" filler — just the paragraph.

If your first lookup returns "no hits", try one more web_lookup with a
different phrasing before giving up.`

// runResearcher builds a fresh sub-agent and runs it. It reuses the parent
// provider so we only keep one OpenAI client. Each call creates a new agent
// instance so memory/history is isolated per topic.
//
// The trace writer is constructed inside agent.Iterate from env, so this
// sub-agent automatically streams to the same panel session as the parent.
func runResearcher(ctx context.Context, p *openai.Provider, topic, focus string) (string, error) {
	tools := newResearcherTools()
	comps := make([]any, len(tools))
	for i, t := range tools {
		comps[i] = t
	}

	sub := looper.MustNewAgent(p, researcherSystemPrompt, comps...)

	prompt := fmt.Sprintf(
		"Topic: %s\nFocus: %s\n\nResearch this and produce your dense one-paragraph brief.",
		topic, focus,
	)

	res, err := sub.Run(ctx, prompt)
	if err != nil {
		return "", fmt.Errorf("researcher(%s): %w", topic, err)
	}
	return strings.TrimSpace(res.Output), nil
}

// ─── Orchestrator tool: spawn-and-run a research sub-agent ────────────────────

type ResearchTopicIn struct {
	Topic string `json:"topic" jsonschema:"description=The subject to research, e.g. 'quantum computing'"`
	Focus string `json:"focus" jsonschema:"description=The specific angle, e.g. 'current state' or 'risks for finance'"`
}

const orchestratorSystemPrompt = `You are a research orchestrator. You delegate
the actual reading and summarising to specialised sub-agents via the
research_topic tool — never claim knowledge yourself.

Mandatory flow:
  1. Identify every distinct topic the user wants covered.
  2. For EACH topic, call research_topic ONCE. If there are multiple topics,
     issue all of those tool calls in the SAME turn so they can run in
     parallel.
  3. ONLY AFTER all research_topic results are in, write your final reply:
     a short intro line, then one paragraph per topic (use the sub-agent's
     paragraph verbatim if it is already good), then a one-sentence
     comparison if the user asked to compare.

Never call research_topic more than once for the same topic.`

// buildOrchestrator wires the orchestrator agent. The research_topic tool's
// body is the nesting point: it spins up a brand-new sub-agent and runs it
// to completion before returning the sub-agent's text as the tool result.
//
// We track an in-flight counter purely so the stdout log can show the
// "2 sub-agents running in parallel" moment to a CLI watcher.
func buildOrchestrator(p *openai.Provider) *looper.Agent {
	var inFlight int32

	research := tool.MustNewTool(ResearchTopicIn{},
		func(ctx context.Context, in ResearchTopicIn) (string, error) {
			now := atomic.AddInt32(&inFlight, 1)
			defer atomic.AddInt32(&inFlight, -1)
			fmt.Printf("[orchestrator]   ↳ spawning sub-agent for %q (focus=%q) — in-flight=%d\n",
				in.Topic, in.Focus, now)

			brief, err := runResearcher(ctx, p, in.Topic, in.Focus)
			if err != nil {
				fmt.Printf("[orchestrator]   ↳ sub-agent for %q failed: %v\n", in.Topic, err)
				return "", err
			}
			fmt.Printf("[orchestrator]   ↳ sub-agent for %q done (%d chars)\n", in.Topic, len(brief))
			return brief, nil
		},
		tool.ToolConfig{
			Name: "research_topic",
			Description: "Spawn a research sub-agent that uses its own tools (web_lookup, " +
				"summarize, assess_credibility) to produce a dense one-paragraph brief on a " +
				"single topic. Returns the sub-agent's final paragraph as the tool result.",
			Parallel: true,
		},
	)

	return looper.MustNewAgent(p, orchestratorSystemPrompt, research)
}

// ─── main ─────────────────────────────────────────────────────────────────────

func main() {
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		fmt.Fprintln(os.Stderr, "OPENAI_API_KEY is required. Run: set -a && source .env.local && set +a")
		os.Exit(1)
	}

	ctx := context.Background()

	// Single provider instance shared across parent and every sub-agent.
	p := openai.NewProvider(key)

	orchestrator := buildOrchestrator(p)

	question := "Compare quantum computing and homomorphic encryption — give me a one-paragraph brief on each."
	fmt.Printf("[orchestrator] user → %s\n", question)

	res, err := orchestrator.Run(ctx, question)
	if err != nil {
		fmt.Fprintf(os.Stderr, "orchestrator run failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println()
	fmt.Println("──── Final answer ────")
	fmt.Println(res.Output)
	fmt.Println("──────────────────────")
	fmt.Printf("Turns: %d  Status: %s  Cost: $%.6f\n",
		res.Turns, res.Status, res.Cost.TotalUSD)
}
