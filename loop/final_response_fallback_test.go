package loop

import (
	"encoding/json"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/message"
)

// TestExtractFinalResponseOutput_Fallback verifies a final_response call is
// ALWAYS recognized as the close, even when the model omits the `output`
// wrapper — otherwise it falls through to tool execution and errors with
// "unknown tool final_response".
func TestExtractFinalResponseOutput_Fallback(t *testing.T) {
	mk := func(args string) []message.ToolCall {
		return []message.ToolCall{{ID: "f1", Name: "final_response", Arguments: json.RawMessage(args)}}
	}
	cases := map[string]struct {
		args    string
		wantOK  bool
		wantOut string
	}{
		"wrapped output":    {`{"output":{"message":"hi"}}`, true, `{"message":"hi"}`},
		"top-level payload": {`{"message":"hi"}`, true, `{"message":"hi"}`},
		"empty args":        {`{}`, true, `{}`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			out, ok := extractFinalResponseOutput(mk(c.args))
			if ok != c.wantOK || out != c.wantOut {
				t.Errorf("got (%q,%v), want (%q,%v)", out, ok, c.wantOut, c.wantOK)
			}
		})
	}
	// A non-final tool call is not a close.
	if _, ok := extractFinalResponseOutput([]message.ToolCall{{Name: "add_tables"}}); ok {
		t.Error("add_tables must not be recognized as a final_response close")
	}
}
