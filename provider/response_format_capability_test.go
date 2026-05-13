package provider

import (
	"context"
	"testing"
)

// fakeCapable implements both LLMProvider (partially) and the optional
// ResponseFormatCapable interface so we can confirm the type assertion
// the loop uses to gate the native path.
type fakeCapable struct{}

func (fakeCapable) Model() string                                            { return "" }
func (fakeCapable) Chat(context.Context, LLMRequest) (*LLMResponse, error)   { return nil, nil }
func (fakeCapable) ChatStream(context.Context, LLMRequest) (<-chan StreamChunk, error) {
	return nil, nil
}
func (fakeCapable) Translator() Translator { return nil }
func (fakeCapable) SupportsResponseFormat() bool { return true }

type fakeIncapable struct{ fakeCapable }

func (fakeIncapable) SupportsResponseFormat() bool { return false }

// TestSupportsNativeResponseFormat asserts the helper correctly probes
// the optional ResponseFormatCapable interface and reports false when
// the provider doesn't implement it OR implements it returning false.
func TestSupportsNativeResponseFormat(t *testing.T) {
	if !SupportsNativeResponseFormat(fakeCapable{}) {
		t.Error("capable provider should report support=true")
	}
	if SupportsNativeResponseFormat(fakeIncapable{}) {
		t.Error("provider returning false should report support=false")
	}
	if SupportsNativeResponseFormat(nil) {
		t.Error("nil provider should report support=false")
	}
}
