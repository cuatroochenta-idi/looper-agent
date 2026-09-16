package provider

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrStreamIdle marks a stream that went silent: the upstream accepted the
// request and then delivered nothing for longer than the configured idle
// window. It is a transient condition — DefaultRetryClassifier treats it as
// retryable — so a failover or retry chain can spend another attempt on it
// instead of letting the turn hang until the whole request times out.
var ErrStreamIdle = errors.New("stream idle")

// StreamIdleProvider wraps another provider with a silence watchdog on its
// streams. Chat passes straight through: a non-streaming call is already
// bounded by the HTTP client's own timeout. Streaming is the hole this
// closes — a provider that accepts the request and then says nothing holds
// the turn open with no error, no failover and no log line.
//
// The watchdog fires in two different ways because the framework treats the
// two silences differently:
//
//   - Before the first chunk, ChatStream itself returns the error. The
//     failover / retry wrappers only recover on the function-return error,
//     so this is the shape that lets the chain move to the next inner. It
//     mirrors the probe the OpenAI-compatible provider already does.
//   - After the first chunk, the error arrives as one final chunk on the
//     channel. Restarting mid-reply would duplicate the tokens the caller
//     has already seen, which is why the framework never fails over there.
//
// Any chunk resets the timer — content, reasoning, tool fragments, usage.
// A model that thinks for three minutes without emitting a token is silent;
// one that streams its thinking is not.
type StreamIdleProvider struct {
	inner LLMProvider
	idle  time.Duration
}

// WithStreamIdleTimeout wraps inner so its streams fail when no chunk
// arrives for idle. A non-positive idle disables the watchdog and returns
// inner unchanged, so callers can wire it from configuration without
// branching.
func WithStreamIdleTimeout(inner LLMProvider, idle time.Duration) LLMProvider {
	if inner == nil || idle <= 0 {
		return inner
	}
	return &StreamIdleProvider{inner: inner, idle: idle}
}

// Model returns the inner provider's model identifier.
func (p *StreamIdleProvider) Model() string { return p.inner.Model() }

// Translator delegates to the inner provider.
func (p *StreamIdleProvider) Translator() Translator { return p.inner.Translator() }

// Chat passes through untouched.
func (p *StreamIdleProvider) Chat(ctx context.Context, req LLMRequest) (*LLMResponse, error) {
	return p.inner.Chat(ctx, req)
}

// SupportsResponseFormat mirrors the inner's capability so wrapping a
// provider never silently drops native structured output.
func (p *StreamIdleProvider) SupportsResponseFormat() bool {
	return SupportsNativeResponseFormat(p.inner)
}

// idleError builds the sentinel-wrapping error, naming the window that
// elapsed so a log line says how long the upstream was quiet.
func (p *StreamIdleProvider) idleError() error {
	return fmt.Errorf("%w: stream delivered no chunk for %s", ErrStreamIdle, p.idle)
}

// ChatStream opens the inner stream under a derived context and forwards its
// chunks under a silence deadline. Cancelling that context is what actually
// stops the upstream — the wrapper never merely stops reading, which would
// leave the connection open and the tokens billing.
func (p *StreamIdleProvider) ChatStream(ctx context.Context, req LLMRequest) (<-chan StreamChunk, error) {
	sctx, cancel := context.WithCancel(ctx)

	timer := time.NewTimer(p.idle)
	stopTimer := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}

	inner, err := p.openStream(sctx, ctx, req, timer)
	if err != nil {
		stopTimer()
		cancel()
		return nil, err
	}

	// Probe the first chunk before handing the channel back, so a provider
	// that accepts the request and then goes quiet fails where the failover
	// and retry wrappers can still act on it. No latency is added: the
	// caller blocks on the first chunk anyway.
	var first StreamChunk
	select {
	case c, ok := <-inner:
		if !ok {
			// The producer closed without a chunk. There is nothing left to
			// watch, and an empty closed channel is what the caller expects.
			stopTimer()
			cancel()
			closed := make(chan StreamChunk)
			close(closed)
			return closed, nil
		}
		first = c
	case <-timer.C:
		cancel()
		go drainChunks(inner)
		return nil, p.idleError()
	case <-ctx.Done():
		stopTimer()
		cancel()
		return nil, ctx.Err()
	}

	out := make(chan StreamChunk, 64)
	go p.forward(ctx, cancel, timer, stopTimer, inner, first, out)
	return out, nil
}

// openStream starts the inner stream on the derived context, bounded by the
// same idle window: an inner that blocks forever inside ChatStream (the
// OpenAI-compatible provider probes its first chunk there) is exactly the
// silence this watchdog exists for.
func (p *StreamIdleProvider) openStream(sctx, ctx context.Context, req LLMRequest, timer *time.Timer) (<-chan StreamChunk, error) {
	type opened struct {
		ch  <-chan StreamChunk
		err error
	}
	done := make(chan opened, 1)
	go func() {
		ch, err := p.inner.ChatStream(sctx, req)
		done <- opened{ch, err}
	}()

	select {
	case r := <-done:
		// The inner's own error passes through verbatim — the watchdog
		// never relabels a 401 or a 400 as a timeout.
		return r.ch, r.err
	case <-timer.C:
		return nil, p.idleError()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// forward relays the inner stream, resetting the silence deadline on every
// chunk. It owns the derived context from here on and always cancels it.
func (p *StreamIdleProvider) forward(
	ctx context.Context,
	cancel context.CancelFunc,
	timer *time.Timer,
	stopTimer func(),
	inner <-chan StreamChunk,
	first StreamChunk,
	out chan<- StreamChunk,
) {
	defer close(out)
	defer cancel()
	defer stopTimer()

	// emit never blocks past the caller's lifetime: a consumer that stops
	// reading (it saw the final chunk, or gave up) must not strand this
	// goroutine holding the upstream open.
	emit := func(c StreamChunk) bool {
		select {
		case out <- c:
			return true
		case <-ctx.Done():
			return false
		}
	}

	// Provenance of the last chunk seen, so a synthesized idle error is
	// still attributable to the provider and model that went quiet.
	var lastProvider, lastModel, lastKey string
	remember := func(c StreamChunk) {
		if c.ProviderID != "" {
			lastProvider = c.ProviderID
		}
		if c.ModelID != "" {
			lastModel = c.ModelID
		}
		if c.APIKeySuffix != "" {
			lastKey = c.APIKeySuffix
		}
	}

	remember(first)
	sawEnd := first.IsFinal || first.Error != nil
	if !emit(first) {
		return
	}

	for {
		if sawEnd {
			// The reply is complete. A producer taking its time to close
			// the channel is not a silent provider, so the deadline is off
			// from here and we simply drain to the close.
			select {
			case c, ok := <-inner:
				if !ok {
					return
				}
				if !emit(c) {
					return
				}
			case <-ctx.Done():
				return
			}
			continue
		}

		stopTimer()
		timer.Reset(p.idle)

		select {
		case c, ok := <-inner:
			if !ok {
				return
			}
			remember(c)
			if c.IsFinal || c.Error != nil {
				sawEnd = true
			}
			if !emit(c) {
				return
			}
		case <-timer.C:
			cancel()
			go drainChunks(inner)
			emit(StreamChunk{
				IsFinal:      true,
				Error:        p.idleError(),
				ProviderID:   lastProvider,
				ModelID:      lastModel,
				APIKeySuffix: lastKey,
			})
			return
		case <-ctx.Done():
			return
		}
	}
}

// drainChunks reads an abandoned stream to its close so the producer
// goroutine can finish instead of blocking forever on an unread send.
func drainChunks(ch <-chan StreamChunk) {
	for range ch {
	}
}
