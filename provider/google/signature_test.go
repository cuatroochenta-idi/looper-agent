package google

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/provider"
	genai "google.golang.org/genai"
)

// TestStream_CapturesThoughtSignature guards the Gemini 3.x contract:
// every function call returned by the model is paired with an opaque
// ThoughtSignature on its Part, and follow-up requests must echo it
// back or the API rejects them with INVALID_ARGUMENT. processStream is
// the streaming entry point, so the signature has to survive the trip
// into message.ToolCall.
func TestStream_CapturesThoughtSignature(t *testing.T) {
	sig := []byte{0x01, 0x02, 0x03, 0x04, 0x05}
	resp := fakeResp(&genai.Part{
		FunctionCall: &genai.FunctionCall{
			ID:   "call_1",
			Name: "load_skill",
			Args: map[string]any{"names": []string{"prd-spec"}},
		},
		ThoughtSignature: sig,
	})
	ch := make(chan provider.StreamChunk, 8)
	go func() {
		defer close(ch)
		processStream(fakeSeq([]*genai.GenerateContentResponse{resp}), ch, false)
	}()

	chunks := drainStream(ch)

	var got message.ToolCall
	var found bool
	for _, c := range chunks {
		for _, tc := range c.ToolCalls {
			if tc.Name == "load_skill" {
				got = tc
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("load_skill tool call not surfaced; chunks=%+v", chunks)
	}
	if !bytes.Equal(got.Signature, sig) {
		t.Fatalf("Signature lost in streaming FromNative: got %v, want %v", got.Signature, sig)
	}
}

// TestFromNative_CapturesThoughtSignature is the non-streaming twin of
// the test above: Chat() goes through Translator.FromNative on the full
// GenerateContentResponse, and the signature must survive there too.
func TestFromNative_CapturesThoughtSignature(t *testing.T) {
	sig := []byte("opaque-blob-from-gemini-3")
	tr := &Translator{model: "gemini-3-flash-preview"}
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			Content: &genai.Content{
				Role: "model",
				Parts: []*genai.Part{{
					FunctionCall: &genai.FunctionCall{
						ID:   "call_42",
						Name: "create_table",
						Args: map[string]any{"name": "tasks"},
					},
					ThoughtSignature: sig,
				}},
			},
		}},
	}
	out, err := tr.FromNative(resp)
	if err != nil {
		t.Fatalf("FromNative returned error: %v", err)
	}
	if len(out.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(out.ToolCalls))
	}
	if !bytes.Equal(out.ToolCalls[0].Signature, sig) {
		t.Fatalf("Signature lost in FromNative: got %v, want %v", out.ToolCalls[0].Signature, sig)
	}
}

// TestToNative_EchoesThoughtSignature is the load-bearing half of the
// round-trip: when a stored assistant message replays a tool call, the
// Part rebuilt for Gemini must carry the same ThoughtSignature back. If
// this regresses the API returns INVALID_ARGUMENT on the very next turn.
func TestToNative_EchoesThoughtSignature(t *testing.T) {
	sig := []byte("must-echo-this-exact-blob")
	tr := &Translator{model: "gemini-3-flash-preview"}

	msgs := []message.Message{
		message.NewUserMessage("hi"),
		{
			Type:    message.MessageAssistant,
			Content: "",
			ToolCalls: []message.ToolCall{{
				ID:        "call_1",
				Name:      "load_skill",
				Arguments: json.RawMessage(`{"names":["x"]}`),
				Signature: sig,
			}},
		},
		{
			Type:    message.MessageTool,
			Name:    "load_skill",
			Content: `{"ok":true}`,
		},
	}

	native := tr.ToNative("system", msgs, nil).(*genaiRequest)

	// Find the assistant Content with role=model and verify the Part
	// carries both the FunctionCall and the original signature.
	var found bool
	for _, c := range native.Contents {
		if c.Role != "model" {
			continue
		}
		for _, p := range c.Parts {
			if p.FunctionCall == nil {
				continue
			}
			if p.FunctionCall.Name != "load_skill" {
				continue
			}
			if !bytes.Equal(p.ThoughtSignature, sig) {
				t.Fatalf("Signature dropped on echo: got %v, want %v",
					p.ThoughtSignature, sig)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("assistant FunctionCall Part not found in native contents")
	}
}

// TestToNative_NoSignatureLeavesNilOnPart asserts that pre-Gemini-3
// callers (and any provider that doesn't issue signatures) keep working:
// an empty Signature must result in a nil ThoughtSignature on the Part
// so the API request stays byte-identical to the v0.2.2 behavior.
func TestToNative_NoSignatureLeavesNilOnPart(t *testing.T) {
	tr := &Translator{model: "gemini-2.5-flash"}
	msgs := []message.Message{
		{
			Type: message.MessageAssistant,
			ToolCalls: []message.ToolCall{{
				ID:        "call_1",
				Name:      "do_thing",
				Arguments: json.RawMessage(`{}`),
			}},
		},
	}
	native := tr.ToNative("", msgs, nil).(*genaiRequest)
	for _, c := range native.Contents {
		for _, p := range c.Parts {
			if p.FunctionCall == nil {
				continue
			}
			if p.ThoughtSignature != nil {
				t.Fatalf("ThoughtSignature must remain nil when ToolCall.Signature is empty, got %v", p.ThoughtSignature)
			}
		}
	}
}
