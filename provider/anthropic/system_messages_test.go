package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/provider"
)

// roles lists each request message as role:first-text (or role:block-type
// when the first block is not text).
func roles(params anthropic.MessageNewParams) []string {
	var out []string
	for _, m := range params.Messages {
		desc := string(m.Role)
		if len(m.Content) > 0 {
			if b := m.Content[0]; b.OfText != nil {
				desc += ":" + b.OfText.Text
			} else if b.OfToolResult != nil {
				desc += ":result"
			} else if b.OfToolUse != nil {
				desc += ":call"
			}
		}
		out = append(out, desc)
	}
	return out
}

func systemTexts(params anthropic.MessageNewParams) []string {
	var out []string
	for _, b := range params.System {
		out = append(out, b.Text)
	}
	return out
}

// agentHistory is a run whose harness adds a notice before the first turn,
// one after a tool result and one at the end.
func agentHistory() []message.Message {
	return []message.Message{
		message.NewSystemMessage("memory summary"),
		message.NewUserMessage("build"),
		message.NewAssistantMessage("", []message.ToolCall{{ID: "t1", Name: "read", Arguments: json.RawMessage(`{}`)}}),
		message.NewToolResult("t1", "read", "ok", false),
		message.NewSystemMessage("notice 1"),
		message.NewAssistantMessage("working", nil),
		message.NewUserMessage("next"),
		message.NewSystemMessage("notice 2"),
	}
}

func TestToNative_MidConversationSystemMessagesStayInPlace(t *testing.T) {
	for _, model := range []string{"claude-haiku-5-5", "claude-opus-5-5", "claude-sonnet-5-5", "claude-opus-4-8", "us.anthropic.claude-haiku-5-5"} {
		t.Run(model, func(t *testing.T) {
			params := NewProvider("test-key", WithModel(model)).Translator().ToNative("prompt", agentHistory(), nil).(anthropic.MessageNewParams)

			if got := strings.Join(systemTexts(params), "|"); got != "prompt|memory summary" {
				t.Errorf("top-level system = %q, want only the prompt and the notice before the first turn", got)
			}
			want := "user:build,assistant:call,user:result,system:notice 1,assistant:working,user:next,system:notice 2"
			if got := strings.Join(roles(params), ","); got != want {
				t.Errorf("messages = %s\nwant       %s", got, want)
			}
		})
	}
}

func TestToNative_SystemMessagesStayTopLevelOnModelsWithoutSupport(t *testing.T) {
	for _, model := range []string{"claude-sonnet-5", "claude-opus-4-7", "claude-haiku-4-5"} {
		t.Run(model, func(t *testing.T) {
			params := NewProvider("test-key", WithModel(model)).Translator().ToNative("prompt", agentHistory(), nil).(anthropic.MessageNewParams)

			if got := strings.Join(systemTexts(params), "|"); got != "prompt|memory summary|notice 1|notice 2" {
				t.Errorf("top-level system = %q, want every system message there", got)
			}
			for _, r := range roles(params) {
				if strings.HasPrefix(r, "system") {
					t.Errorf("messages carry a system role: %v", roles(params))
				}
			}
		})
	}
}

func TestToNative_SystemMessageWhereTheAPIRejectsTheRole(t *testing.T) {
	history := []message.Message{
		message.NewUserMessage("build"),
		message.NewAssistantMessage("done", nil),
		message.NewSystemMessage("not finished yet"),
		message.NewAssistantMessage("", []message.ToolCall{{ID: "t1", Name: "read", Arguments: json.RawMessage(`{}`)}}),
		message.NewSystemMessage("between call and result"),
		message.NewToolResult("t1", "read", "ok", false),
		message.NewAssistantMessage("all done", nil),
	}

	params := NewProvider("test-key", WithModel("claude-haiku-5-5")).Translator().ToNative("prompt", history, nil).(anthropic.MessageNewParams)

	// After an assistant turn the role is a 400, so the notice becomes a
	// user turn; between a tool call and its result it waits for the result.
	want := "user:build,assistant:done,user:<system-reminder>\nnot finished yet\n</system-reminder>," +
		"assistant:call,user:result,system:between call and result,assistant:all done"
	if got := strings.Join(roles(params), ","); got != want {
		t.Errorf("messages = %q\nwant       %q", got, want)
	}
	if got := strings.Join(systemTexts(params), "|"); got != "prompt" {
		t.Errorf("top-level system = %q, want it untouched", got)
	}
}

func TestNewParams_GatesSystemMessagesOnTheRequestModel(t *testing.T) {
	p := NewProvider("test-key", WithModel("claude-sonnet-5"))
	params := p.newParams(provider.LLMRequest{Model: "claude-haiku-5-5", SystemPrompt: "prompt", Messages: agentHistory()})

	if got := strings.Join(systemTexts(params), "|"); got != "prompt|memory summary" {
		t.Errorf("top-level system = %q, want the request model's (mid-conversation) placement", got)
	}
}

func TestCacheMessages_SkipsATrailingSystemMessage(t *testing.T) {
	history := append([]message.Message{message.NewUserMessage("build")}, toolTurn("t1")...)
	history = append(history, message.NewSystemMessage("notice"))

	params := messagesProvider().Translator().ToNative("sys", history, nil).(anthropic.MessageNewParams)

	last := len(params.Messages) - 1
	if params.Messages[last].Role != anthropic.MessageParamRoleSystem {
		t.Fatalf("last message role = %s, want system", params.Messages[last].Role)
	}
	got := cachedBlocks(params)
	if len(got) != 1 || len(got[last-1]) != 1 {
		t.Errorf("cached blocks = %v, want one on the tool result before the system message", got)
	}
}
