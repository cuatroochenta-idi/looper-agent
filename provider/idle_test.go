package provider

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// scriptedStream is a provider whose stream the test drives: it emits the
// scripted chunks with the given gaps, and records whether the context it
// was opened under ended up cancelled.
type scriptedStream struct {
	model string

	openDelay time.Duration
	gap       time.Duration
	chunks    []StreamChunk
	// hang makes the stream go silent after the scripted chunks instead of
	// closing, which is the failure the watchdog exists for.
	hang bool

	cancelled atomic.Bool
	opens     atomic.Int32
}

func (s *scriptedStream) Model() string          { return s.model }
func (s *scriptedStream) Translator() Translator { return nil }

func (s *scriptedStream) Chat(_ context.Context, _ LLMRequest) (*LLMResponse, error) {
	return &LLMResponse{Content: "ok", IsFinal: true}, nil
}

func (s *scriptedStream) ChatStream(ctx context.Context, _ LLMRequest) (<-chan StreamChunk, error) {
	s.opens.Add(1)
	if s.openDelay > 0 {
		select {
		case <-time.After(s.openDelay):
		case <-ctx.Done():
			s.cancelled.Store(true)
			return nil, ctx.Err()
		}
	}

	ch := make(chan StreamChunk)
	go func() {
		defer close(ch)
		for _, c := range s.chunks {
			if s.gap > 0 {
				select {
				case <-time.After(s.gap):
				case <-ctx.Done():
					s.cancelled.Store(true)
					return
				}
			}
			select {
			case ch <- c:
			case <-ctx.Done():
				s.cancelled.Store(true)
				return
			}
		}
		if s.hang {
			<-ctx.Done()
			s.cancelled.Store(true)
		}
	}()
	return ch, nil
}

func drainStream(t *testing.T, ch <-chan StreamChunk) []StreamChunk {
	t.Helper()
	var out []StreamChunk
	for c := range ch {
		out = append(out, c)
	}
	return out
}

func TestWithStreamIdleTimeout_PassthroughWhenDisabled(t *testing.T) {
	inner := &scriptedStream{model: "m"}
	if got := WithStreamIdleTimeout(inner, 0); got != LLMProvider(inner) {
		t.Errorf("idle=0 should return the inner unchanged, got %T", got)
	}
	if got := WithStreamIdleTimeout(inner, -time.Second); got != LLMProvider(inner) {
		t.Errorf("negative idle should return the inner unchanged, got %T", got)
	}
}

// TestWithStreamIdleTimeout_ChunksFlowing: a stream that keeps talking is
// never interrupted, and every chunk arrives with its provenance intact.
func TestWithStreamIdleTimeout_ChunksFlowing(t *testing.T) {
	inner := &scriptedStream{
		model: "m",
		gap:   5 * time.Millisecond,
		chunks: []StreamChunk{
			{Content: "a", ProviderID: "openrouter", ModelID: "deepseek"},
			{Content: "b", ProviderID: "openrouter", ModelID: "deepseek"},
			{Content: "ab", IsFinal: true, ProviderID: "openrouter", ModelID: "deepseek", Usage: &Usage{OutputTokens: 2}},
		},
	}
	p := WithStreamIdleTimeout(inner, 200*time.Millisecond)

	ch, err := p.ChatStream(context.Background(), LLMRequest{})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	got := drainStream(t, ch)

	if len(got) != 3 {
		t.Fatalf("forwarded %d chunks, want 3: %+v", len(got), got)
	}
	for i, c := range got {
		if c.Error != nil {
			t.Errorf("chunk %d carries an error: %v", i, c.Error)
		}
		if c.ProviderID != "openrouter" || c.ModelID != "deepseek" {
			t.Errorf("chunk %d lost its provenance: %+v", i, c)
		}
	}
	if !got[2].IsFinal || got[2].Usage == nil {
		t.Errorf("final chunk mangled: %+v", got[2])
	}
}

// TestWithStreamIdleTimeout_MidStreamSilence: once the caller has seen
// tokens the framework never fails over, so the idle error arrives as one
// final chunk — and the upstream context is cancelled so the connection
// stops billing.
func TestWithStreamIdleTimeout_MidStreamSilence(t *testing.T) {
	inner := &scriptedStream{
		model:  "m",
		chunks: []StreamChunk{{Content: "a", ProviderID: "openrouter", ModelID: "deepseek"}},
		hang:   true,
	}
	p := WithStreamIdleTimeout(inner, 60*time.Millisecond)

	ch, err := p.ChatStream(context.Background(), LLMRequest{})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	got := drainStream(t, ch)

	if len(got) != 2 {
		t.Fatalf("forwarded %d chunks, want the content chunk plus the idle final: %+v", len(got), got)
	}
	last := got[len(got)-1]
	if !last.IsFinal {
		t.Errorf("idle chunk is not final: %+v", last)
	}
	if !errors.Is(last.Error, ErrStreamIdle) {
		t.Fatalf("last.Error = %v, want it to wrap ErrStreamIdle", last.Error)
	}
	if last.ProviderID != "openrouter" || last.ModelID != "deepseek" {
		t.Errorf("idle chunk lost the provenance of the stream that went quiet: %+v", last)
	}

	waitFor(t, func() bool { return inner.cancelled.Load() }, time.Second,
		"upstream context was never cancelled")
}

// TestWithStreamIdleTimeout_PreFirstChunkSilenceIsASyncError: silence before
// the first chunk has to surface as the function-return error, which is the
// only shape the failover and retry wrappers act on.
func TestWithStreamIdleTimeout_PreFirstChunkSilenceIsASyncError(t *testing.T) {
	inner := &scriptedStream{model: "m", hang: true}
	p := WithStreamIdleTimeout(inner, 50*time.Millisecond)

	ch, err := p.ChatStream(context.Background(), LLMRequest{})
	if ch != nil {
		t.Errorf("expected a nil channel on pre-first-chunk silence, got %T", ch)
	}
	if !errors.Is(err, ErrStreamIdle) {
		t.Fatalf("err = %v, want it to wrap ErrStreamIdle", err)
	}
	waitFor(t, func() bool { return inner.cancelled.Load() }, time.Second,
		"upstream context was never cancelled")
}

// TestWithStreamIdleTimeout_BlockingOpenIsBounded: the OpenAI-compatible
// provider probes its first chunk inside ChatStream, so an upstream that
// goes quiet makes the OPEN call block rather than the channel. The window
// has to cover that too, or the watchdog misses the commonest shape.
func TestWithStreamIdleTimeout_BlockingOpenIsBounded(t *testing.T) {
	inner := &scriptedStream{model: "m", openDelay: 5 * time.Second}
	p := WithStreamIdleTimeout(inner, 50*time.Millisecond)

	start := time.Now()
	ch, err := p.ChatStream(context.Background(), LLMRequest{})
	if ch != nil {
		t.Errorf("expected a nil channel, got %T", ch)
	}
	if !errors.Is(err, ErrStreamIdle) {
		t.Fatalf("err = %v, want it to wrap ErrStreamIdle", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("waited %s for a 50ms window — the open call was not bounded", elapsed)
	}
	waitFor(t, func() bool { return inner.cancelled.Load() }, 2*time.Second,
		"upstream context was never cancelled")
}

// TestWithStreamIdleTimeout_ReasoningKeepsTheStreamAlive: a model streaming
// nothing but its thinking is working, not silent. This is what makes the
// watchdog safe to point at open-weights reasoning models.
func TestWithStreamIdleTimeout_ReasoningKeepsTheStreamAlive(t *testing.T) {
	chunks := make([]StreamChunk, 0, 9)
	for i := 0; i < 8; i++ {
		chunks = append(chunks, StreamChunk{Reasoning: "thinking "})
	}
	chunks = append(chunks, StreamChunk{Content: "42", IsFinal: true})

	inner := &scriptedStream{model: "m", gap: 10 * time.Millisecond, chunks: chunks}
	// The whole think (80ms) outlasts the idle window; no single gap does.
	p := WithStreamIdleTimeout(inner, 50*time.Millisecond)

	ch, err := p.ChatStream(context.Background(), LLMRequest{})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	got := drainStream(t, ch)

	if len(got) != len(chunks) {
		t.Fatalf("forwarded %d chunks, want %d", len(got), len(chunks))
	}
	for i, c := range got {
		if c.Error != nil {
			t.Fatalf("chunk %d carries an error — a thinking model read as silent: %v", i, c.Error)
		}
	}
}

// TestWithStreamIdleTimeout_InnerErrorsPassThrough: the watchdog never
// relabels the inner's own failure as a timeout.
func TestWithStreamIdleTimeout_InnerErrorsPassThrough(t *testing.T) {
	boom := errors.New("openai stream: 401 unauthorized")
	inner := &failingStream{err: boom}
	p := WithStreamIdleTimeout(inner, time.Second)

	_, err := p.ChatStream(context.Background(), LLMRequest{})
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want the inner's own error", err)
	}
	if errors.Is(err, ErrStreamIdle) {
		t.Error("inner error was relabelled as an idle timeout")
	}
}

type failingStream struct{ err error }

func (f *failingStream) Model() string          { return "failing" }
func (f *failingStream) Translator() Translator { return nil }
func (f *failingStream) Chat(context.Context, LLMRequest) (*LLMResponse, error) {
	return nil, f.err
}
func (f *failingStream) ChatStream(context.Context, LLMRequest) (<-chan StreamChunk, error) {
	return nil, f.err
}

// TestWithStreamIdleTimeout_FailoverEngages is the payoff: wrapped inside a
// FailoverProvider, a silent primary costs one attempt instead of the whole
// turn, and the secondary answers.
func TestWithStreamIdleTimeout_FailoverEngages(t *testing.T) {
	silent := &scriptedStream{model: "silent", hang: true}
	healthy := &scriptedStream{model: "healthy", chunks: []StreamChunk{
		{Content: "42", IsFinal: true, ProviderID: "backup", ModelID: "healthy"},
	}}

	chain, err := NewFailover([]LLMProvider{
		WithStreamIdleTimeout(silent, 50*time.Millisecond),
		healthy,
	}, WithFailoverNames([]string{"silent", "healthy"}))
	if err != nil {
		t.Fatalf("NewFailover: %v", err)
	}

	ch, err := chain.ChatStream(context.Background(), LLMRequest{})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	got := drainStream(t, ch)

	if len(got) != 1 || got[0].Content != "42" {
		t.Fatalf("failover did not reach the healthy inner: %+v", got)
	}
	if !got[0].Fallback {
		t.Error("chunk from a non-primary inner should be flagged Fallback")
	}
	if healthy.opens.Load() != 1 {
		t.Errorf("healthy inner opened %d times, want 1", healthy.opens.Load())
	}
}

// TestDefaultRetryClassifier_StreamIdleIsTransient: without this, the retry
// wrapper would treat a silent stream as a permanent failure and give up.
func TestDefaultRetryClassifier_StreamIdleIsTransient(t *testing.T) {
	err := (&StreamIdleProvider{idle: 4 * time.Minute}).idleError()
	if got := DefaultRetryClassifier(err); got != Transient {
		t.Errorf("DefaultRetryClassifier(ErrStreamIdle) = %v, want Transient", got)
	}
}

// TestWithStreamIdleTimeout_ConsumerStopsReading: a caller that walks away
// must not strand the forwarding goroutine holding the upstream open.
func TestWithStreamIdleTimeout_ConsumerStopsReading(t *testing.T) {
	chunks := make([]StreamChunk, 0, 64)
	for i := 0; i < 64; i++ {
		chunks = append(chunks, StreamChunk{Content: "x"})
	}
	inner := &scriptedStream{model: "m", chunks: chunks, hang: true}
	p := WithStreamIdleTimeout(inner, time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := p.ChatStream(ctx, LLMRequest{})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	// Read one chunk, then abandon the stream the way a cancelled run does.
	<-ch
	cancel()

	waitFor(t, func() bool { return inner.cancelled.Load() }, 2*time.Second,
		"abandoning the stream left the upstream running")
}

func waitFor(t *testing.T, cond func() bool, limit time.Duration, msg string) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal(msg)
}
