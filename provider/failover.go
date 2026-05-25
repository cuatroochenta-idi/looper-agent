package provider

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// ErrAllProvidersFailed wraps the last underlying error after every inner
// provider in a FailoverProvider has failed. Use errors.Is to surface a
// "service unavailable" message to end users without confusing it with a
// single-provider blip.
var ErrAllProvidersFailed = errors.New("all providers failed")

// FailoverProvider implements LLMProvider on top of an ordered list of
// inner providers (primary → secondary → …). Each call tries inner[0];
// on a non-context error it logs the switch and tries inner[1], and so
// on. When every inner fails, the call returns an error that satisfies
// errors.Is(err, ErrAllProvidersFailed) wrapping the last underlying
// error.
//
// Streaming follows RetryProvider.ChatStream semantics: failover only
// happens before the stream opens. Once the channel is being drained,
// errors mid-stream bubble up — restarting on a different provider would
// duplicate already-emitted tokens.
//
// Relationship with ProviderQueue: ProviderQueue exposes Execute(fn),
// which is a caller-driven primitive that does not implement LLMProvider
// and therefore cannot be passed to looper.NewAgent. FailoverProvider IS
// an LLMProvider; pass it to looper.NewAgent and the agent loop benefits
// from cross-provider failover transparently. Use ProviderQueue directly
// when you need an external dispatch loop (e.g. running the same prompt
// against every provider for comparison); use FailoverProvider when you
// want failover folded into the standard agent loop.
type FailoverProvider struct {
	inners []LLMProvider
	names  []string
}

// FailoverOption mutates a FailoverProvider during construction.
type FailoverOption func(*FailoverProvider)

// WithFailoverNames attaches a parallel slice of labels used in the
// switch / recovery / exhaustion telemetry logs. The slice must have the
// same length as inners; otherwise NewFailover returns an error. When
// omitted, labels default to the inners' Model() identifiers, which is
// usually enough for debugging but can be ambiguous when multiple inners
// share the same model.
func WithFailoverNames(names []string) FailoverOption {
	return func(f *FailoverProvider) {
		f.names = names
	}
}

// NewFailover constructs a FailoverProvider. Returns an error when
// inners is empty, when WithFailoverNames was supplied with a mismatched
// length, or when any inner is nil.
func NewFailover(inners []LLMProvider, opts ...FailoverOption) (*FailoverProvider, error) {
	if len(inners) == 0 {
		return nil, errors.New("provider.NewFailover: empty inner list")
	}
	for i, p := range inners {
		if p == nil {
			return nil, fmt.Errorf("provider.NewFailover: nil provider at index %d", i)
		}
	}
	f := &FailoverProvider{inners: inners}
	for _, opt := range opts {
		opt(f)
	}
	if f.names == nil {
		f.names = make([]string, len(inners))
		for i, p := range inners {
			f.names[i] = p.Model()
		}
	}
	if len(f.names) != len(inners) {
		return nil, fmt.Errorf("provider.NewFailover: names (%d) and inners (%d) length mismatch", len(f.names), len(inners))
	}
	return f, nil
}

// Model returns the first inner's model identifier as a stable label.
// Callers should treat the return value as informative only — the inner
// that ultimately answered a given request depends on the failover path.
func (f *FailoverProvider) Model() string { return f.inners[0].Model() }

// Translator delegates to the first inner. The framework never calls
// Translator on the LLMProvider exposed to NewAgent — it lives on the
// interface for native-format inspection — so a stable choice is fine.
// Each inner uses its own translator internally inside Chat/ChatStream.
func (f *FailoverProvider) Translator() Translator { return f.inners[0].Translator() }

// SupportsResponseFormat reports the conservative AND across every
// inner: the wrapper can only promise native structured output when
// every fallback target also supports it. Without this, switching to a
// non-capable inner mid-request would silently drop ResponseSchema.
func (f *FailoverProvider) SupportsResponseFormat() bool {
	for _, p := range f.inners {
		if !SupportsNativeResponseFormat(p) {
			return false
		}
	}
	return true
}

// Chat tries each inner in declared order. Context cancellation is
// honoured before every attempt and short-circuits the iteration — we
// don't try the next inner once the caller has given up.
//
// When inner[N] (N > 0) answers successfully, Fallback=true is stamped
// on the response so downstream consumers (loop telemetry, web UI) can
// tell apart "this call hit the primary" from "this call hit the
// failover path". ProviderID/ModelID propagate transparently from
// whichever inner answered.
func (f *FailoverProvider) Chat(ctx context.Context, req LLMRequest) (*LLMResponse, error) {
	var lastErr error
	for i, p := range f.inners {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		resp, err := p.Chat(ctx, req)
		if err == nil {
			if i > 0 {
				slog.Info("provider.failover.recovered",
					slog.String("provider", f.names[i]),
					slog.Int("attempt", i+1),
				)
				if resp != nil {
					resp.Fallback = true
				}
			}
			return resp, nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		lastErr = err
		if i < len(f.inners)-1 {
			slog.Warn("provider.failover.switch",
				slog.String("from", f.names[i]),
				slog.String("to", f.names[i+1]),
				slog.String("error", err.Error()),
			)
		}
	}
	slog.Error("provider.failover.exhausted",
		slog.Int("providers", len(f.inners)),
		slog.String("last_error", lastErr.Error()),
	)
	return nil, fmt.Errorf("%w: %w", ErrAllProvidersFailed, lastErr)
}

// ChatStream tries each inner in declared order, but only before the
// stream opens. Once the channel is being drained, mid-stream errors
// bubble up — restarting on a different inner would duplicate already-
// emitted tokens.
//
// When inner[N] (N > 0) opens successfully, the returned channel is
// wrapped so Fallback=true is stamped on every chunk. Same purpose as
// the non-streaming path: downstream consumers can attribute the call
// to the failover branch without inspecting logs.
func (f *FailoverProvider) ChatStream(ctx context.Context, req LLMRequest) (<-chan StreamChunk, error) {
	var lastErr error
	for i, p := range f.inners {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		ch, err := p.ChatStream(ctx, req)
		if err == nil {
			if i > 0 {
				slog.Info("provider.failover.recovered",
					slog.String("provider", f.names[i]),
					slog.Int("attempt", i+1),
					slog.Bool("stream", true),
				)
				return stampFallback(ch), nil
			}
			return ch, nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		lastErr = err
		if i < len(f.inners)-1 {
			slog.Warn("provider.failover.switch",
				slog.String("from", f.names[i]),
				slog.String("to", f.names[i+1]),
				slog.String("error", err.Error()),
				slog.Bool("stream", true),
			)
		}
	}
	slog.Error("provider.failover.exhausted",
		slog.Int("providers", len(f.inners)),
		slog.String("last_error", lastErr.Error()),
		slog.Bool("stream", true),
	)
	return nil, fmt.Errorf("%w: %w", ErrAllProvidersFailed, lastErr)
}

// stampFallback wraps a stream channel so every chunk carries
// Fallback=true. Used when a non-primary inner answers — the loop sees
// fallback provenance on the final chunk (where Usage lives) and can
// attribute tokens / mark the trace correctly.
func stampFallback(inner <-chan StreamChunk) <-chan StreamChunk {
	out := make(chan StreamChunk, cap(inner))
	go func() {
		defer close(out)
		for c := range inner {
			c.Fallback = true
			out <- c
		}
	}()
	return out
}
