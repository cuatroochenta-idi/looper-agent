package provider

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// scriptedProvider returns errors / responses from a pre-baked sequence,
// so retry tests can drive deterministic transient → permanent → success
// transitions without timing flakiness.
type scriptedProvider struct {
	calls  atomic.Int32
	script []error
	ok     *LLMResponse
}

func (p *scriptedProvider) Model() string          { return "scripted" }
func (p *scriptedProvider) Translator() Translator { return nil }

func (p *scriptedProvider) Chat(_ context.Context, _ LLMRequest) (*LLMResponse, error) {
	idx := int(p.calls.Add(1)) - 1
	if idx < len(p.script) && p.script[idx] != nil {
		return nil, p.script[idx]
	}
	if p.ok != nil {
		return p.ok, nil
	}
	return &LLMResponse{Content: "ok", IsFinal: true}, nil
}

func (p *scriptedProvider) ChatStream(ctx context.Context, req LLMRequest) (<-chan StreamChunk, error) {
	ch := make(chan StreamChunk, 1)
	resp, err := p.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	go func() {
		defer close(ch)
		ch <- StreamChunk{Content: resp.Content, IsFinal: true}
	}()
	return ch, nil
}

// TestRetryProvider_RetriesTransientThenSucceeds asserts the basic
// promise: 2 transient errors followed by success → 3 total Chat calls,
// final response is returned.
func TestRetryProvider_RetriesTransientThenSucceeds(t *testing.T) {
	inner := &scriptedProvider{
		script: []error{
			errors.New("503 service unavailable"),
			errors.New("connection reset by peer"),
		},
	}
	rp := NewRetryProvider(inner, RetryConfig{
		MaxAttempts:    5,
		InitialBackoff: time.Millisecond,
	})

	resp, err := rp.Chat(context.Background(), LLMRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("unexpected response: %+v", resp)
	}
	if inner.calls.Load() != 3 {
		t.Errorf("expected 3 attempts (2 transient + 1 success), got %d", inner.calls.Load())
	}
}

// TestRetryProvider_PermanentErrorNoRetry asserts the classifier does NOT
// retry "permanent" errors — a 400 / 401 / 403 should fail fast.
func TestRetryProvider_PermanentErrorNoRetry(t *testing.T) {
	inner := &scriptedProvider{
		script: []error{errors.New("401 unauthorized")},
	}
	rp := NewRetryProvider(inner, RetryConfig{MaxAttempts: 5, InitialBackoff: time.Microsecond})

	_, err := rp.Chat(context.Background(), LLMRequest{})
	if err == nil {
		t.Fatal("expected the permanent error to surface")
	}
	if inner.calls.Load() != 1 {
		t.Errorf("permanent error should not retry, got %d attempts", inner.calls.Load())
	}
}

// TestRetryProvider_CustomClassifier asserts users can plug their own
// retry policy — useful for SDK-specific error types (anthropic SDK,
// openai SDK) whose strings don't match our defaults.
func TestRetryProvider_CustomClassifier(t *testing.T) {
	inner := &scriptedProvider{
		script: []error{errors.New("custom-please-retry"), errors.New("custom-please-retry")},
	}
	rp := NewRetryProvider(inner, RetryConfig{
		MaxAttempts:    3,
		InitialBackoff: time.Microsecond,
		Classify: func(err error) RetryDecision {
			if err != nil && err.Error() == "custom-please-retry" {
				return Transient
			}
			return Permanent
		},
	})

	_, err := rp.Chat(context.Background(), LLMRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inner.calls.Load() != 3 {
		t.Errorf("custom classifier should drive 3 calls, got %d", inner.calls.Load())
	}
}

// TestRetryProvider_RespectsContextCancel asserts retry loop honors
// context cancellation between attempts — a cancelled run aborts fast
// instead of churning through the backoff schedule.
func TestRetryProvider_RespectsContextCancel(t *testing.T) {
	inner := &scriptedProvider{
		script: []error{errors.New("503"), errors.New("503"), errors.New("503")},
	}
	rp := NewRetryProvider(inner, RetryConfig{
		MaxAttempts:    10,
		InitialBackoff: 50 * time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	_, err := rp.Chat(ctx, LLMRequest{})
	if err == nil {
		t.Fatal("expected error after cancel")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

// TestRetryProvider_CircuitOpensAfterThreshold asserts the breaker
// trips after CircuitFailureThreshold consecutive transient failures —
// subsequent calls return ErrCircuitOpen without hitting inner.
func TestRetryProvider_CircuitOpensAfterThreshold(t *testing.T) {
	inner := &scriptedProvider{
		script: []error{
			// Two retry-cycles that each exhaust attempts → 2 "trips"
			// → threshold of 2 opens the circuit.
			errors.New("503"), errors.New("503"),
			errors.New("503"), errors.New("503"),
		},
	}
	rp := NewRetryProvider(inner, RetryConfig{
		MaxAttempts:             2,
		InitialBackoff:          time.Microsecond,
		CircuitFailureThreshold: 2,
		CircuitCooldown:         50 * time.Millisecond,
	})

	_, _ = rp.Chat(context.Background(), LLMRequest{})
	_, _ = rp.Chat(context.Background(), LLMRequest{})

	// Circuit should now be open. The next call must NOT touch inner.
	prev := inner.calls.Load()
	_, err := rp.Chat(context.Background(), LLMRequest{})
	if err == nil || !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected ErrCircuitOpen, got %v", err)
	}
	if inner.calls.Load() != prev {
		t.Errorf("open circuit must not call inner, calls went %d -> %d", prev, inner.calls.Load())
	}
}

// TestRetryProvider_CircuitClosesAfterCooldown asserts the breaker
// releases after CircuitCooldown elapses and a single success closes
// the gate fully.
func TestRetryProvider_CircuitClosesAfterCooldown(t *testing.T) {
	inner := &scriptedProvider{
		// Single failure trips the breaker, then the provider recovers
		// and returns ok for every subsequent call.
		script: []error{errors.New("503")},
		ok:     &LLMResponse{Content: "back", IsFinal: true},
	}
	rp := NewRetryProvider(inner, RetryConfig{
		MaxAttempts:             1, // no retry, single trip
		InitialBackoff:          time.Microsecond,
		CircuitFailureThreshold: 1,
		CircuitCooldown:         20 * time.Millisecond,
	})

	_, _ = rp.Chat(context.Background(), LLMRequest{})
	if !errors.Is(must(rp.Chat(context.Background(), LLMRequest{})), ErrCircuitOpen) {
		t.Fatal("expected circuit open immediately after first failure with threshold=1")
	}

	time.Sleep(40 * time.Millisecond)

	resp, err := rp.Chat(context.Background(), LLMRequest{})
	if err != nil {
		t.Fatalf("post-cooldown call should succeed, got %v", err)
	}
	if resp.Content != "back" {
		t.Errorf("unexpected response post-cooldown: %+v", resp)
	}
}

// must is a tiny helper that returns the error from a (*LLMResponse, error)
// call so tests can chain assertions concisely.
func must(_ *LLMResponse, err error) error { return err }

// avoid "fmt imported and not used" if we drop a Printf later.
var _ = fmt.Sprintf
