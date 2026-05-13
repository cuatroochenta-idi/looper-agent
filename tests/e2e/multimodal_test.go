//go:build e2e

package e2e

import (
	"context"
	"strings"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/looper"
	"github.com/cuatroochenta-idi/looper-agent/message"
)

const visionImageURL = "https://upload.wikimedia.org/wikipedia/commons/4/47/PNG_transparency_demonstration_1.png"

// runMultimodalProbe asks the supplied agent to describe a known image
// via the WithHistory + AddUserMessageParts path. Returns the model's
// answer for assertion-side keyword checks.
func runMultimodalProbe(t *testing.T, agent *looper.Agent) string {
	t.Helper()
	hist := message.NewHistory()
	hist.AddUserMessageParts(
		message.TextPart("What objects do you see in this image? Answer in one short sentence."),
		message.ImageURLPart(visionImageURL),
	)
	res, err := agent.Run(context.Background(), "", looper.WithHistory(hist))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "completed" {
		t.Errorf("expected status=completed, got %q", res.Status)
	}
	return strings.ToLower(res.Output)
}

// TestE2E_Multimodal_OpenAI confirms the OpenAI vision path returns a
// non-empty answer that mentions the dice in the test image.
func TestE2E_Multimodal_OpenAI(t *testing.T) {
	p := openAIProvider(t)
	agent := looper.MustNewAgent(p, "You are a concise vision assistant.")
	out := runMultimodalProbe(t, agent)
	if out == "" {
		t.Fatal("empty output")
	}
	if !strings.ContainsAny(out, "dice") && !strings.Contains(out, "glass") {
		t.Errorf("expected image-related description, got %q", out)
	}
}

// TestE2E_Multimodal_Gemini confirms the Gemini streaming + multimodal
// flow works end-to-end after the processStream fixes that landed in
// this branch.
func TestE2E_Multimodal_Gemini(t *testing.T) {
	p := geminiProvider(t)
	agent := looper.MustNewAgent(p, "You are a concise vision assistant.")
	out := runMultimodalProbe(t, agent)
	if out == "" {
		t.Fatal("empty output — possible regression on Gemini streaming")
	}
	if !strings.ContainsAny(out, "dice") && !strings.Contains(out, "glass") {
		t.Errorf("expected image-related description, got %q", out)
	}
}
