package loop

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/provider"
	"github.com/cuatroochenta-idi/looper-agent/tool"
)

// closeWithSibling is a turn where the model calls a real tool and the
// structured-output final_response in the same response.
func closeWithSibling(output string) *provider.LLMResponse {
	args, _ := json.Marshal(map[string]string{"output": output})

	return &provider.LLMResponse{
		// The mock streams one chunk per response and marks it final only
		// when IsFinal is set.
		IsFinal: true,
		ToolCalls: []message.ToolCall{
			{ID: "form1", Name: "ask_user_form", Arguments: json.RawMessage(`{}`)},
			{ID: "fr1", Name: "final_response", Arguments: args},
		},
		Usage: provider.Usage{InputTokens: 5, OutputTokens: 3},
	}
}

type siblingCloseFixture struct {
	loop     *AgentLoop
	formRuns atomic.Int32
	hookSaw  []string
}

// newSiblingCloseFixture wires a loop whose first turn mixes ask_user_form
// with final_response and whose second turn closes alone. Output validation
// has no retry budget and rejects every candidate except "done", so any
// validation of the mixed close would end the run.
func newSiblingCloseFixture(t *testing.T) *siblingCloseFixture {
	t.Helper()

	f := &siblingCloseFixture{}
	form := tool.MustNewTool(struct{}{},
		func(_ context.Context, _ struct{}) (string, error) {
			f.formRuns.Add(1)

			return "form created", nil
		},
		tool.ToolConfig{Name: "ask_user_form", Description: "ask the user"},
	)
	prov := &mockProvider{
		model: "mock",
		responses: []*provider.LLMResponse{
			closeWithSibling("premature"),
			finalResponseClose("done"),
		},
	}
	onlyDone := func(raw []byte) error {
		if string(raw) != `"done"` {
			return errors.New("not done")
		}

		return nil
	}
	validator := TurnValidatorFunc(func(_ context.Context, snap TurnSnapshot) Outcome {
		for _, tc := range snap.ToolCalls {
			if tc.Name == finalResponseToolName && snap.Turn == 0 {
				return Outcome{OK: false, Reason: "saw_close", Hint: "the mixed close reached the validator"}
			}
		}

		return Outcome{OK: true}
	})

	f.loop = NewAgentLoop(
		prov,
		func(_ context.Context) string { return "sys" },
		[]*tool.Tool{form},
		WithLoopStructuredOutput(json.RawMessage(`{"type":"string"}`)),
		WithLoopOutputValidation(onlyDone, 0, nil),
		WithLoopTurnValidator(validator, 0),
		WithLoopMaxTurns(5),
	)
	f.loop.HookManager().OnBeforeToolExecution(func(_ context.Context, p *ToolExecutionParams) error {
		for _, c := range p.Calls {
			f.hookSaw = append(f.hookSaw, c.Name)
		}

		return nil
	})

	return f
}

func (f *siblingCloseFixture) assertHistory(t *testing.T, history *message.History) {
	t.Helper()

	if got := f.formRuns.Load(); got != 1 {
		t.Errorf("ask_user_form ran %d times, want 1", got)
	}
	if len(f.hookSaw) != 1 || f.hookSaw[0] != "ask_user_form" {
		t.Errorf("BeforeToolExecution saw %v, want only [ask_user_form]", f.hookSaw)
	}

	var formResult, closeResult *message.Message
	for _, m := range history.Messages() {
		if m.Type != message.MessageTool {
			continue
		}
		switch m.ToolID {
		case "form1":
			formResult = &m
		case "fr1":
			closeResult = &m
		}
	}
	if formResult == nil || formResult.Content != "form created" {
		t.Errorf("ask_user_form result = %+v, want the tool's output", formResult)
	}
	if closeResult == nil || !strings.Contains(closeResult.Content, "Not closed") {
		t.Errorf("final_response result = %+v, want the refusal", closeResult)
	}
}

func TestStructuredClose_WithSiblingCallsRunsThemAndRefusesTheClose(t *testing.T) {
	f := newSiblingCloseFixture(t)

	res, err := f.loop.Run(context.Background(), "build")
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if res.Status != "completed" || res.Output != `"done"` || res.Turns != 2 {
		t.Errorf("got Status=%q Output=%q Turns=%d, want completed/\"done\"/2", res.Status, res.Output, res.Turns)
	}
	f.assertHistory(t, res.History)
}

func TestStructuredClose_WithSiblingCallsRunsThemAndRefusesTheClose_Iterator(t *testing.T) {
	f := newSiblingCloseFixture(t)

	it := f.loop.Iterate(context.Background(), "build")
	var refusalStep bool
	for step := range it.Next() {
		if step.Type == StepError {
			t.Fatalf("unexpected error step: %v", step.Error)
		}
		if step.Type == StepToolResult && step.ToolCallID == "fr1" && step.IsError {
			refusalStep = true
		}
	}

	res := it.Result()
	if res.Status != "completed" || res.Output != `"done"` || res.Turns != 2 {
		t.Errorf("got Status=%q Output=%q Turns=%d, want completed/\"done\"/2", res.Status, res.Output, res.Turns)
	}
	if !refusalStep {
		t.Error("the refusal was not emitted as an error tool_result step")
	}
	f.assertHistory(t, res.History)
}

func TestStructuredClose_AloneStillCloses_Iterator(t *testing.T) {
	prov := &mockProvider{
		model:     "mock",
		responses: []*provider.LLMResponse{finalResponseClose("immediate")},
	}
	lp := NewAgentLoop(
		prov,
		func(_ context.Context) string { return "sys" },
		nil,
		WithLoopStructuredOutput(json.RawMessage(`{"type":"object"}`)),
		WithLoopMaxTurns(5),
	)

	it := lp.Iterate(context.Background(), "build")
	for range it.Next() {
	}

	res := it.Result()
	if res.Output != `"immediate"` || res.Turns != 1 || res.Status != "completed" {
		t.Errorf("got Output=%q Turns=%d Status=%q, want \"immediate\"/1/completed", res.Output, res.Turns, res.Status)
	}
}
