package provider

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// ErrAllKeysFailed wraps the last underlying error after every key in a
// KeyRotationProvider has been exhausted. Callers can errors.Is against
// it to distinguish "this whole provider slot ran out of keys" from "the
// failover chain exhausted every provider type" (ErrAllProvidersFailed).
var ErrAllKeysFailed = errors.New("all api keys exhausted")

// KeyRotationProvider implements LLMProvider on top of several inner
// providers that share a provider type — typically the same SDK
// instantiated with different API keys, all hitting the same model. On a
// non-context error from key N it sleeps for retryDelay and tries key
// N+1; when every key has failed it returns an error that satisfies
// errors.Is(err, ErrAllKeysFailed) wrapping the last underlying error.
//
// Use it to spread quota across multiple API keys for the same upstream
// provider (e.g. several Google Gemini keys) or to dodge per-key 429s
// without falling over to a different provider type. Compose with
// FailoverProvider when the chain crosses provider types:
//
//	geminiPool, _ := provider.NewKeyRotation(geminiInners, 750*time.Millisecond)
//	openaiPool, _ := provider.NewKeyRotation(openaiInners, 750*time.Millisecond)
//	chain, _     := provider.NewFailover(
//	    []provider.LLMProvider{openaiPool, geminiPool},
//	    provider.WithFailoverNames([]string{"openai", "gemini"}),
//	)
//	agent := looper.MustNewAgent(chain, sysPrompt)
type KeyRotationProvider struct {
	inners     []LLMProvider
	label      string
	retryDelay time.Duration
}

// KeyRotationOption mutates a KeyRotationProvider during construction.
type KeyRotationOption func(*KeyRotationProvider)

// WithKeyRotationLabel attaches a string label used in telemetry logs.
// Defaults to the first inner's Model() identifier. Useful to disambiguate
// several rotation pools sharing the same model.
func WithKeyRotationLabel(label string) KeyRotationOption {
	return func(k *KeyRotationProvider) {
		k.label = label
	}
}

// NewKeyRotation constructs a rotator. inners must be non-empty and free
// of nil entries. retryDelay < 0 is clamped to zero; zero means "try the
// next key immediately on failure".
func NewKeyRotation(inners []LLMProvider, retryDelay time.Duration, opts ...KeyRotationOption) (*KeyRotationProvider, error) {
	if len(inners) == 0 {
		return nil, errors.New("provider.NewKeyRotation: empty inner list")
	}
	for i, p := range inners {
		if p == nil {
			return nil, fmt.Errorf("provider.NewKeyRotation: nil provider at index %d", i)
		}
	}
	if retryDelay < 0 {
		retryDelay = 0
	}
	k := &KeyRotationProvider{inners: inners, retryDelay: retryDelay}
	for _, opt := range opts {
		opt(k)
	}
	if k.label == "" {
		k.label = inners[0].Model()
	}
	return k, nil
}

// Model returns the first inner's model identifier. Every inner in a
// rotation pool shares the same model — only the API key varies — so
// the first is representative.
func (k *KeyRotationProvider) Model() string { return k.inners[0].Model() }

// Translator delegates to the first inner. All inners share the same
// provider type and therefore the same translator implementation.
func (k *KeyRotationProvider) Translator() Translator { return k.inners[0].Translator() }

// SupportsResponseFormat reports the first inner's capability. All
// inners share the provider type, so the answer is the same regardless
// of which key is active.
func (k *KeyRotationProvider) SupportsResponseFormat() bool {
	return SupportsNativeResponseFormat(k.inners[0])
}

// Chat tries each key in order, sleeping retryDelay between attempts.
// Context cancellation is honoured both before each attempt and during
// the sleep — a cancelled caller never waits for the next attempt.
func (k *KeyRotationProvider) Chat(ctx context.Context, req LLMRequest) (*LLMResponse, error) {
	var lastErr error
	for i, p := range k.inners {
		if i > 0 {
			if err := waitOrCancel(ctx, k.retryDelay); err != nil {
				return nil, err
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		resp, err := p.Chat(ctx, req)
		if err == nil {
			if i > 0 {
				slog.Info("provider.key_rotation.recovered",
					slog.String("label", k.label),
					slog.Int("key_index", i),
				)
			}
			return resp, nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		lastErr = err
		if i < len(k.inners)-1 {
			slog.Warn("provider.key_rotation.switch",
				slog.String("label", k.label),
				slog.Int("from_key_index", i),
				slog.Int("to_key_index", i+1),
				slog.String("error", err.Error()),
			)
		}
	}
	slog.Error("provider.key_rotation.exhausted",
		slog.String("label", k.label),
		slog.Int("keys", len(k.inners)),
		slog.String("last_error", lastErr.Error()),
	)
	return nil, fmt.Errorf("%w (label %q, %d keys): %w", ErrAllKeysFailed, k.label, len(k.inners), lastErr)
}

// ChatStream tries each key in order before the stream opens. Mid-stream
// errors are not failed over: a partially-drained channel cannot be
// safely restarted on a different key without duplicating tokens.
func (k *KeyRotationProvider) ChatStream(ctx context.Context, req LLMRequest) (<-chan StreamChunk, error) {
	var lastErr error
	for i, p := range k.inners {
		if i > 0 {
			if err := waitOrCancel(ctx, k.retryDelay); err != nil {
				return nil, err
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		ch, err := p.ChatStream(ctx, req)
		if err == nil {
			if i > 0 {
				slog.Info("provider.key_rotation.recovered",
					slog.String("label", k.label),
					slog.Int("key_index", i),
					slog.Bool("stream", true),
				)
			}
			return ch, nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		lastErr = err
		if i < len(k.inners)-1 {
			slog.Warn("provider.key_rotation.switch",
				slog.String("label", k.label),
				slog.Int("from_key_index", i),
				slog.Int("to_key_index", i+1),
				slog.String("error", err.Error()),
				slog.Bool("stream", true),
			)
		}
	}
	slog.Error("provider.key_rotation.exhausted",
		slog.String("label", k.label),
		slog.Int("keys", len(k.inners)),
		slog.String("last_error", lastErr.Error()),
		slog.Bool("stream", true),
	)
	return nil, fmt.Errorf("%w (label %q, %d keys): %w", ErrAllKeysFailed, k.label, len(k.inners), lastErr)
}

// waitOrCancel sleeps for d unless ctx finishes first. d <= 0 is a no-op.
// Exposed at package scope (not on a receiver) so it stays a single
// reusable helper across rotation / future middleware that needs the
// same pattern.
func waitOrCancel(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
