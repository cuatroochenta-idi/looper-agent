package anthropic

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/tool"
)

// helper that builds a Provider configured with the requested cache
// breakpoints and returns the JSON payload its translator would send to
// the Anthropic API.
func marshalWithBreakpoints(t *testing.T, systemPrompt string, breakpoints []string, tools []*tool.Tool) string {
	t.Helper()
	opts := []Option{}
	if len(breakpoints) > 0 {
		opts = append(opts, WithCacheBreakpoints(breakpoints...))
	}
	p := NewProvider("test-key", opts...)
	native := p.Translator().ToNative(systemPrompt, nil, tools)
	b, err := json.Marshal(native)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// TestCache_NoBreakpoints_OmitsCacheControl asserts the legacy (off-by-
// default) behavior: when WithCacheBreakpoints is not set the payload
// carries no cache_control markers at all.
func TestCache_NoBreakpoints_OmitsCacheControl(t *testing.T) {
	got := marshalWithBreakpoints(t, "you are a helper", nil, nil)
	if strings.Contains(got, "cache_control") {
		t.Errorf("default payload should have no cache_control, got %s", got)
	}
}

// TestCache_SystemBreakpoint_MarksLastSystemBlock asserts that a "system"
// breakpoint produces cache_control on the last system text block. The
// Anthropic API caches the prefix up to that marker — i.e. system prompt +
// preceding tools become reusable on subsequent calls.
func TestCache_SystemBreakpoint_MarksLastSystemBlock(t *testing.T) {
	got := marshalWithBreakpoints(t,
		"you are a helper", []string{CacheSystemPrompt}, nil)
	if !strings.Contains(got, `"cache_control":{"type":"ephemeral"}`) {
		t.Errorf("system breakpoint should add ephemeral cache_control, got %s", got)
	}
}

// TestCache_ToolsBreakpoint_MarksLastTool asserts that a "tools"
// breakpoint puts cache_control on the LAST tool — the order-sensitive
// Anthropic semantics that lets the cache cover system + all tools.
func TestCache_ToolsBreakpoint_MarksLastTool(t *testing.T) {
	a := tool.MustNewTool(struct{}{},
		func(_ context.Context, _ struct{}) (string, error) { return "", nil },
		tool.ToolConfig{Name: "a"})
	b := tool.MustNewTool(struct{}{},
		func(_ context.Context, _ struct{}) (string, error) { return "", nil },
		tool.ToolConfig{Name: "b"})

	got := marshalWithBreakpoints(t,
		"helper", []string{CacheTools}, []*tool.Tool{a, b})
	// One cache_control marker, attached to the second tool.
	count := strings.Count(got, `"cache_control":{"type":"ephemeral"}`)
	if count != 1 {
		t.Errorf("tools breakpoint should emit exactly 1 cache_control, got %d in %s", count, got)
	}
	// The marker should be co-located with tool "b" (the last one).
	bIdx := strings.LastIndex(got, `"name":"b"`)
	cacheIdx := strings.LastIndex(got, `"cache_control":{"type":"ephemeral"}`)
	if bIdx == -1 || cacheIdx == -1 {
		t.Fatalf("missing markers in payload: %s", got)
	}
	// Both should sit inside the same tool object — close enough proxy:
	// they are within ~200 bytes of each other.
	if cacheIdx < bIdx-300 || cacheIdx > bIdx+300 {
		t.Errorf("cache_control should be co-located with last tool 'b', got bIdx=%d cacheIdx=%d", bIdx, cacheIdx)
	}
}

// TestCache_BothBreakpoints_EmitsTwoMarkers asserts the multi-breakpoint
// case: system + tools simultaneously produces two cache_control markers
// (Anthropic allows up to 4 per request).
func TestCache_BothBreakpoints_EmitsTwoMarkers(t *testing.T) {
	a := tool.MustNewTool(struct{}{},
		func(_ context.Context, _ struct{}) (string, error) { return "", nil },
		tool.ToolConfig{Name: "a"})

	got := marshalWithBreakpoints(t,
		"helper", []string{CacheSystemPrompt, CacheTools}, []*tool.Tool{a})
	count := strings.Count(got, `"cache_control":{"type":"ephemeral"}`)
	if count != 2 {
		t.Errorf("expected 2 cache markers, got %d in %s", count, got)
	}
}

// keep the message import alive so go-test doesn't whine if a later edit
// removes the only test using it.
var _ = message.MessageUser
