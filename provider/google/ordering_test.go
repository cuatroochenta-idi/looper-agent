package google

import (
	"encoding/json"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/message"
)

// TestToNative_MergesConsecutiveAssistantTurns guards the Gemini
// constraint that no two Contents may share a role back to back. When
// the loop persists assistant text and assistant tool calls as separate
// messages (which Anthropic and OpenAI happily accept) the translator
// has to fold them into one Content; otherwise Gemini rejects the
// request with:
//
//	Error 400: Please ensure that function call turn comes
//	immediately after a user turn or after a function response turn.
func TestToNative_MergesConsecutiveAssistantTurns(t *testing.T) {
	tr := &Translator{model: "gemini-3.5-flash"}
	msgs := []message.Message{
		message.NewUserMessage("hi"),
		{Type: message.MessageAssistant, Content: "Let me think out loud."},
		{
			Type: message.MessageAssistant,
			ToolCalls: []message.ToolCall{{
				ID: "call_1", Name: "load_skill",
				Arguments: json.RawMessage(`{"names":["x"]}`),
			}},
		},
	}
	native := tr.ToNative("", msgs, nil).(*genaiRequest)
	if len(native.Contents) != 2 {
		t.Fatalf("expected 2 Contents (user + merged model), got %d", len(native.Contents))
	}
	if native.Contents[0].Role != "user" {
		t.Fatalf("Contents[0].Role = %q, want user", native.Contents[0].Role)
	}
	if native.Contents[1].Role != "model" {
		t.Fatalf("Contents[1].Role = %q, want model", native.Contents[1].Role)
	}
	parts := native.Contents[1].Parts
	if len(parts) != 2 {
		t.Fatalf("merged model Content should hold 2 parts (text + functionCall), got %d", len(parts))
	}
	if parts[0].Text == "" {
		t.Errorf("merged model[0] should be the text part, got %+v", parts[0])
	}
	if parts[1].FunctionCall == nil {
		t.Errorf("merged model[1] should be the function call, got %+v", parts[1])
	}
}

// TestToNative_MergesConsecutiveToolResults handles parallel-tool
// turns: when the assistant fires N tool calls and the loop reports
// each result as a separate MessageTool, the translator must collapse
// them into one Content with role="user". Gemini treats functionResponse
// as a user-role turn and rejects N adjacent user Contents in a row.
func TestToNative_MergesConsecutiveToolResults(t *testing.T) {
	tr := &Translator{model: "gemini-3.5-flash"}
	msgs := []message.Message{
		message.NewUserMessage("kick off"),
		{
			Type: message.MessageAssistant,
			ToolCalls: []message.ToolCall{
				{ID: "a", Name: "f1", Arguments: json.RawMessage(`{}`)},
				{ID: "b", Name: "f2", Arguments: json.RawMessage(`{}`)},
			},
		},
		{Type: message.MessageTool, Name: "f1", Content: "ok-1"},
		{Type: message.MessageTool, Name: "f2", Content: "ok-2"},
	}
	native := tr.ToNative("", msgs, nil).(*genaiRequest)

	// Sequence must be: user, model, user — three Contents, with the
	// two function responses folded into the final user turn.
	if len(native.Contents) != 3 {
		t.Fatalf("expected 3 Contents, got %d: %+v", len(native.Contents), native.Contents)
	}
	roles := []string{native.Contents[0].Role, native.Contents[1].Role, native.Contents[2].Role}
	want := []string{"user", "model", "user"}
	for i := range roles {
		if roles[i] != want[i] {
			t.Fatalf("Contents[%d].Role = %q, want %q (full: %v)", i, roles[i], want[i], roles)
		}
	}
	respParts := native.Contents[2].Parts
	if len(respParts) != 2 {
		t.Fatalf("merged tool-response Content should hold 2 functionResponse parts, got %d", len(respParts))
	}
	for i, p := range respParts {
		if p.FunctionResponse == nil {
			t.Errorf("respParts[%d] missing FunctionResponse: %+v", i, p)
		}
	}
}

// TestToNative_PreservesAlternatingTurns is the no-op case: a clean
// user → model → user → model history must not be touched by the
// merge logic. Asserting Contents stays N=4 ensures the new code path
// triggers only when adjacent roles match.
func TestToNative_PreservesAlternatingTurns(t *testing.T) {
	tr := &Translator{model: "gemini-3.5-flash"}
	msgs := []message.Message{
		message.NewUserMessage("u1"),
		{Type: message.MessageAssistant, Content: "a1"},
		message.NewUserMessage("u2"),
		{Type: message.MessageAssistant, Content: "a2"},
	}
	native := tr.ToNative("", msgs, nil).(*genaiRequest)
	if len(native.Contents) != 4 {
		t.Fatalf("alternating turns must stay separate; got %d Contents: %+v",
			len(native.Contents), native.Contents)
	}
}
