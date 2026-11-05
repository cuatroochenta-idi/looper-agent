package anthropic

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/cuatroochenta-idi/looper-agent/message"
)

// cachedBlocks lists, per message, the indexes of the content blocks that
// carry cache_control.
func cachedBlocks(params anthropic.MessageNewParams) map[int][]int {
	out := map[int][]int{}
	for i, m := range params.Messages {
		for j, b := range m.Content {
			if cc := b.GetCacheControl(); cc != nil && cc.Type != "" {
				out[i] = append(out[i], j)
			}
		}
	}

	return out
}

func toolTurn(id string) []message.Message {
	return []message.Message{
		message.NewAssistantMessage("", []message.ToolCall{{ID: id, Name: "read", Arguments: json.RawMessage(`{}`)}}),
		message.NewToolResult(id, "read", "ok", false),
	}
}

func messagesProvider() *Provider {
	return NewProvider("test-key", WithModel("claude-haiku-5-5"),
		WithCacheBreakpoints(CacheSystemPrompt, CacheTools, CacheMessages))
}

func TestCacheMessages_MarksTheLastBlockOfTheConversation(t *testing.T) {
	history := append([]message.Message{message.NewUserMessage("build a crm")}, toolTurn("t1")...)
	history = append(history, toolTurn("t2")...)

	params := messagesProvider().Translator().ToNative("sys", history, nil).(anthropic.MessageNewParams)

	last := len(params.Messages) - 1
	want := map[int][]int{last: {len(params.Messages[last].Content) - 1}}
	if got := cachedBlocks(params); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("cached blocks = %v, want %v (only the last block of the last message)", got, want)
	}
}

func TestCacheMessages_OffByDefault(t *testing.T) {
	p := NewProvider("test-key", WithCacheBreakpoints(CacheSystemPrompt, CacheTools))
	history := append([]message.Message{message.NewUserMessage("hi")}, toolTurn("t1")...)

	params := p.Translator().ToNative("sys", history, nil).(anthropic.MessageNewParams)
	if got := cachedBlocks(params); len(got) != 0 {
		t.Errorf("cached blocks = %v, want none without CacheMessages", got)
	}
}

func TestCacheMessages_SecondBreakpointWhenATurnOutrunsTheLookback(t *testing.T) {
	parts := make([]message.Part, 25)
	for i := range parts {
		parts[i] = message.TextPart(fmt.Sprintf("chunk %d", i))
	}
	history := append([]message.Message{message.NewUserMessage("start")}, toolTurn("t1")...)
	history = append(history,
		message.NewAssistantMessage("reading", nil),
		message.NewUserMessageWithParts(parts...),
	)

	params := messagesProvider().Translator().ToNative("sys", history, nil).(anthropic.MessageNewParams)

	last := len(params.Messages) - 1
	got := cachedBlocks(params)
	if len(got[last]) != 1 || got[last][0] != len(parts)-1 {
		t.Errorf("last message breakpoints = %v, want only its last block", got[last])
	}
	// The previous request ended with the tool result: that is where its
	// moving breakpoint wrote the cache entry this one must read.
	prev := last - 2
	if params.Messages[prev].Content[0].OfToolResult == nil || len(got[prev]) != 1 {
		t.Errorf("breakpoints = %v, want one on the previous request's last block (message %d)", got, prev)
	}

	total := len(got[last]) + len(got[prev])
	if total+2 > maxCacheBreakpoints {
		t.Errorf("%d message breakpoints plus system and tools exceed the limit of %d", total, maxCacheBreakpoints)
	}
}

func TestToNative_ParallelToolResultsShareOneUserMessage(t *testing.T) {
	history := []message.Message{
		message.NewUserMessage("go"),
		message.NewAssistantMessage("", []message.ToolCall{
			{ID: "a", Name: "read", Arguments: json.RawMessage(`{}`)},
			{ID: "b", Name: "read", Arguments: json.RawMessage(`{}`)},
		}),
		message.NewToolResult("a", "read", "1", false),
		message.NewToolResult("b", "read", "2", false),
	}

	params := NewProvider("test-key").Translator().ToNative("", history, nil).(anthropic.MessageNewParams)
	if len(params.Messages) != 3 {
		t.Fatalf("messages = %d, want 3 (user, assistant, one user turn with both results)", len(params.Messages))
	}
	results := params.Messages[2].Content
	if len(results) != 2 || results[0].OfToolResult.ToolUseID != "a" || results[1].OfToolResult.ToolUseID != "b" {
		t.Errorf("last user turn = %+v, want the results of a and b in order", results)
	}
}

func TestToNative_ToolUseInputReplaysByteIdentical(t *testing.T) {
	args := json.RawMessage(`{"zeta":1,"alpha":{"y":"<b>","b":[3,1]}}`)
	history := []message.Message{
		message.NewUserMessage("go"),
		message.NewAssistantMessage("", []message.ToolCall{{ID: "a", Name: "write", Arguments: args}}),
		message.NewToolResult("a", "write", "ok", false),
	}
	p := NewProvider("test-key")

	first, err := json.Marshal(p.Translator().ToNative("", history, nil))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	stored, err := json.Marshal(history)
	if err != nil {
		t.Fatalf("marshal history: %v", err)
	}
	var restored []message.Message
	if err := json.Unmarshal(stored, &restored); err != nil {
		t.Fatalf("unmarshal history: %v", err)
	}
	second, err := json.Marshal(p.Translator().ToNative("", restored, nil))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if !bytes.Equal(first, second) {
		t.Errorf("request changed after a history round trip:\n%s\n%s", first, second)
	}
	if !strings.Contains(string(first), `"input":{"zeta":1,"alpha":{"y":"`) {
		t.Errorf("tool_use input reordered or rewritten: %s", first)
	}
}
