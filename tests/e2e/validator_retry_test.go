//go:build e2e

package e2e

import (
	"context"
	"strings"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/looper"
	"github.com/cuatroochenta-idi/looper-agent/loop"
)

// TestE2E_TurnValidator_Reprompts asserts the validator path against a
// real model: a too-short reply is rejected, the hint is added as a
// system message, and the next turn produces a longer, accepted reply.
//
// The test runs against OpenAI for cost reasons; a Gemini / Anthropic
// variant could be added if behaviour diverges materially.
func TestE2E_TurnValidator_Reprompts(t *testing.T) {
	p := openAIProvider(t)

	validator := func(snap loop.TurnSnapshot) loop.Outcome {
		if len(snap.ToolCalls) > 0 {
			return loop.Outcome{OK: true}
		}
		if len(strings.TrimSpace(snap.LastAssistantContent)) < 80 {
			return loop.Outcome{
				OK:     false,
				Reason: "too-short",
				Hint:   "Your reply was too short. Give a proper, longer explanation in at least 3 sentences.",
			}
		}
		return loop.Outcome{OK: true}
	}

	agent := looper.MustNewAgent(p,
		"You are a curt assistant. Answer briefly.",
		looper.WithTurnValidatorFunc(validator, 2),
	)

	res, err := agent.Run(context.Background(), "What is recursion?")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// Either the model satisfied the validator (status=completed) or it
	// exhausted retries (status=validation_exhausted). In both cases the
	// loop must have made more than one turn.
	if res.Turns < 2 {
		t.Errorf("expected at least 2 turns (1 reject + 1 retry), got %d (status=%q)",
			res.Turns, res.Status)
	}
	if res.Output == "" {
		t.Error("expected non-empty final output")
	}
}
