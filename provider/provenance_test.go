package provider

import (
	"context"
	"errors"
	"testing"
)

// TestFailoverProvider_StampsFallbackOnNonPrimary asserts the contract
// "Fallback=true when a non-primary inner answers" — the signal the
// agent loop relies on to attribute LLM calls to the failover path.
func TestFailoverProvider_StampsFallbackOnNonPrimary(t *testing.T) {
	primary := &stubFailoverProvider{
		name:    "primary",
		chatErr: errors.New("503"),
	}
	secondary := &stubFailoverProvider{
		name: "secondary",
	}
	f, _ := NewFailover(
		[]LLMProvider{primary, secondary},
		WithFailoverNames([]string{"primary", "secondary"}),
	)
	resp, err := f.Chat(context.Background(), LLMRequest{})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if !resp.Fallback {
		t.Errorf("Fallback = false, want true (secondary answered after primary failed)")
	}
}

// TestFailoverProvider_PrimarySuccessLeavesFallbackZero asserts that when
// the primary succeeds, the response goes through with whatever Fallback
// the primary set (zero in the stub) — the wrapper does not flip it.
func TestFailoverProvider_PrimarySuccessLeavesFallbackZero(t *testing.T) {
	primary := &stubFailoverProvider{name: "primary"}
	secondary := &stubFailoverProvider{name: "secondary"}
	f, _ := NewFailover(
		[]LLMProvider{primary, secondary},
		WithFailoverNames([]string{"primary", "secondary"}),
	)
	resp, err := f.Chat(context.Background(), LLMRequest{})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Fallback {
		t.Errorf("Fallback = true, want false (primary answered)")
	}
}

// TestFailoverProvider_StreamFallbackOnNonPrimary mirrors the Chat-path
// assertion for the streaming path: every chunk of the inner that
// answered must carry Fallback=true when that inner wasn't the primary.
func TestFailoverProvider_StreamFallbackOnNonPrimary(t *testing.T) {
	primary := &stubFailoverProvider{
		name:      "primary",
		streamErr: errors.New("connection reset"),
	}
	secondary := &stubFailoverProvider{name: "secondary"}
	f, _ := NewFailover(
		[]LLMProvider{primary, secondary},
		WithFailoverNames([]string{"primary", "secondary"}),
	)
	ch, err := f.ChatStream(context.Background(), LLMRequest{})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	chunk := <-ch
	if !chunk.Fallback {
		t.Errorf("chunk.Fallback = false, want true (secondary opened the stream)")
	}
}

// TestLLMResponseFields verifies the new provenance fields exist on the
// response struct so the wire shape is part of the type's contract.
// Compile-time check, not a behavioural one — fails the build if anyone
// removes the fields by accident.
func TestLLMResponseFields(t *testing.T) {
	var resp LLMResponse
	resp.ProviderID = "openai"
	resp.ModelID = "gpt-x"
	resp.Fallback = true
	_ = resp
}

// TestStreamChunkFields — same guarantee for StreamChunk.
func TestStreamChunkFields(t *testing.T) {
	var c StreamChunk
	c.ProviderID = "google"
	c.ModelID = "gemini-x"
	c.Fallback = true
	_ = c
}
