package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/message"
)

// An assistant turn answered by Anthropic keeps its signed thinking blocks.
// After a failover the OpenAI-compatible translators must not send them.
func TestToNative_IgnoresAnthropicThinkingBlocks(t *testing.T) {
	assistant := message.NewAssistantMessage("ok", []message.ToolCall{{ID: "c1", Name: "read", Arguments: json.RawMessage(`{}`)}})
	assistant.ThinkingBlocks = []message.ThinkingBlock{{Provider: "anthropic", Type: "thinking", Thinking: "plan", Signature: "anthropic-sig"}}
	history := []message.Message{message.NewUserMessage("go"), assistant, message.NewToolResult("c1", "read", "1", false)}

	for name, tr := range map[string]*Translator{
		"plain":             {model: "gpt-6"},
		"open model echoes": {model: "m", openModel: &OpenModelReasoning{Wire: ReasoningWireOpenRouter, PassBack: true}},
	} {
		raw, err := json.Marshal(tr.ToNative("", history, nil))
		if err != nil {
			t.Fatalf("%s: marshal: %v", name, err)
		}
		if strings.Contains(string(raw), "anthropic-sig") || strings.Contains(string(raw), "plan") {
			t.Errorf("%s: request carries the Anthropic thinking block: %s", name, raw)
		}
	}
}
