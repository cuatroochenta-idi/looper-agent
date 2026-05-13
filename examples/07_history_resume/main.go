// Example: persist a conversation to disk and resume it later.
//
// The framework keeps the entire conversation as a `*message.History`, which
// serializes to JSON out of the box. You can stash it in any backend (file,
// SQL, Redis) and feed it back into a future run with `looper.WithHistory`.
//
// This example writes /tmp/looper-history.json on the first turn, then loads
// it back and continues — the agent remembers what was said.
//
// Usage:
//
//	set -a && source .env.local && set +a
//	go run examples/07_history_resume/main.go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/cuatroochenta-idi/looper-agent/looper"
	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/provider/openai"
)

const historyPath = "/tmp/looper-history.json"

func main() {
	ctx := context.Background()

	p := openai.NewProvider(os.Getenv("OPENAI_API_KEY"))
	agent := looper.MustNewAgent(p,
		"You are a helpful assistant. Be concise.",
	)

	// ── Turn 1: introduce a fact the agent should remember. ───────────────────
	fmt.Println("=== Turn 1 (fresh history) ===")
	res1, err := agent.Run(ctx, "My favourite colour is teal. Please remember it.")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Run 1: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Output: %s\n", res1.Output)
	fmt.Printf("Cost:   $%.6f  Messages: %d\n\n", res1.Cost.TotalUSD, res1.History.Len())

	// Persist the history as JSON.
	data, err := json.MarshalIndent(res1.History, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(historyPath, data, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Persisted %d bytes of history to %s\n\n", len(data), historyPath)

	// ── Restart "from a new process": rebuild the agent and reload history. ───
	raw, err := os.ReadFile(historyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read: %v\n", err)
		os.Exit(1)
	}
	restored, err := message.UnmarshalHistory(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "unmarshal: %v\n", err)
		os.Exit(1)
	}

	freshAgent := looper.MustNewAgent(p, "You are a helpful assistant. Be concise.")

	// ── Turn 2: agent should still know the colour. ───────────────────────────
	fmt.Println("=== Turn 2 (restored history) ===")
	res2, err := freshAgent.Run(ctx,
		"What did I tell you was my favourite colour?",
		looper.WithHistory(restored),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Run 2: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Output: %s\n", res2.Output)
	fmt.Printf("Cost:   $%.6f  Messages: %d\n", res2.Cost.TotalUSD, res2.History.Len())
}
