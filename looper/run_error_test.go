package looper

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/provider"
)

var errUpstream = errors.New("bedrock: ValidationException: bad request")

// failingStreamProvider reports a provider failure the way the streaming
// path does: not by returning an error from ChatStream, but by putting one
// on a chunk.
type failingStreamProvider struct{}

func (failingStreamProvider) Model() string                   { return "stub" }
func (failingStreamProvider) Translator() provider.Translator { return nil }

func (failingStreamProvider) Chat(_ context.Context, _ provider.LLMRequest) (*provider.LLMResponse, error) {
	return nil, errUpstream
}

func (failingStreamProvider) ChatStream(_ context.Context, _ provider.LLMRequest) (<-chan provider.StreamChunk, error) {
	ch := make(chan provider.StreamChunk, 1)
	ch <- provider.StreamChunk{IsFinal: true, Error: errUpstream}
	close(ch)
	return ch, nil
}

// TestRunPropagatesStreamError guards a failure mode that made every
// provider error on the streaming path invisible: Agent.Run drains the
// iterator for side effects, and the iterator surfaces failures as
// StepError steps rather than by closing with an error. Discarding the
// steps meant Run returned (RunResult{Output: ""}, nil) — a failed call
// that looked like a successful empty one, with the real cause gone.
func TestRunPropagatesStreamError(t *testing.T) {
	agent := MustNewAgent(failingStreamProvider{}, "sys")

	res, err := agent.Run(context.Background(), "hello")
	if err == nil {
		t.Fatalf("want the upstream error, got a silent success: %+v", res)
	}
	if !errors.Is(err, errUpstream) {
		t.Errorf("error = %v, want it to wrap %v", err, errUpstream)
	}
	if !strings.Contains(err.Error(), "ValidationException") {
		t.Errorf("error %q should carry the upstream detail", err)
	}
}
