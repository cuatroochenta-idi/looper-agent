package provider

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"
)

// RetryDecision classifies whether an error from an underlying provider
// is worth retrying. Used by RetryProvider's classifier.
type RetryDecision int

const (
	// Permanent — fail fast, no retry. Auth errors, malformed requests,
	// model-not-found, etc.
	Permanent RetryDecision = iota

	// Transient — retry with backoff. 5xx, rate limits, network blips.
	Transient
)

// ErrCircuitOpen is returned when the circuit breaker is tripped. The
// inner provider is NOT called — the error fails fast so upstream
// failover (ProviderQueue) can take over without waiting on a hung API.
var ErrCircuitOpen = errors.New("retry-provider: circuit breaker open")

// RetryConfig drives a RetryProvider's retry + circuit-breaker behavior.
// Zero values use sensible defaults so callers can opt in incrementally.
type RetryConfig struct {
	// MaxAttempts is the total number of Chat calls per request (including
	// the first). Defaults to 3.
	MaxAttempts int

	// InitialBackoff is the wait before the second attempt. Defaults to
	// 500ms. The schedule is geometric: cap × BackoffFactor each turn,
	// up to MaxBackoff, with Jitter applied per attempt.
	InitialBackoff time.Duration

	// MaxBackoff caps the per-attempt wait. Defaults to 30s.
	MaxBackoff time.Duration

	// BackoffFactor multiplies the wait between attempts. Defaults to 2.0
	// (exponential).
	BackoffFactor float64

	// Jitter is a fractional 0..1 that randomizes each wait
	// in [base*(1-Jitter), base*(1+Jitter)]. Defaults to 0.2.
	Jitter float64

	// Classify lets callers override the default error classifier. Use
	// it to teach the provider SDK-specific error types. The default
	// inspects the error text for common transient patterns (5xx, 429,
	// timeout, EOF, connection refused, etc.).
	Classify func(error) RetryDecision

	// CircuitFailureThreshold is the number of CONSECUTIVE failed
	// requests (after exhausting retries) that trip the breaker. Zero
	// disables the breaker entirely.
	CircuitFailureThreshold int

	// CircuitCooldown is how long the breaker stays open after tripping.
	// Defaults to 30s when the breaker is enabled.
	CircuitCooldown time.Duration
}

// RetryProvider wraps any LLMProvider with retry + circuit-breaker logic.
// Implements LLMProvider itself so it composes with ProviderQueue and
// other middleware-style wrappers transparently.
type RetryProvider struct {
	inner LLMProvider
	cfg   RetryConfig
	rng   *rand.Rand
	rngMu sync.Mutex

	// circuit-breaker state
	mu          sync.Mutex
	consecFails int
	openedAt    time.Time
}

// NewRetryProvider constructs a RetryProvider around inner with the given
// config. Zero-value fields fill in with sensible defaults.
func NewRetryProvider(inner LLMProvider, cfg RetryConfig) *RetryProvider {
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 3
	}
	if cfg.InitialBackoff <= 0 {
		cfg.InitialBackoff = 500 * time.Millisecond
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = 30 * time.Second
	}
	if cfg.BackoffFactor <= 1 {
		cfg.BackoffFactor = 2.0
	}
	if cfg.Jitter < 0 {
		cfg.Jitter = 0
	}
	if cfg.Jitter > 1 {
		cfg.Jitter = 1
	}
	if cfg.Jitter == 0 {
		cfg.Jitter = 0.2
	}
	if cfg.Classify == nil {
		cfg.Classify = DefaultRetryClassifier
	}
	if cfg.CircuitFailureThreshold > 0 && cfg.CircuitCooldown <= 0 {
		cfg.CircuitCooldown = 30 * time.Second
	}
	return &RetryProvider{
		inner: inner,
		cfg:   cfg,
		// Seed-per-instance keeps multiple RetryProviders in the same
		// process from synchronizing their jitter.
		rng: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Model delegates to the inner provider.
func (p *RetryProvider) Model() string { return p.inner.Model() }

// Translator delegates to the inner provider.
func (p *RetryProvider) Translator() Translator { return p.inner.Translator() }

// SupportsResponseFormat propagates the inner provider's capability so
// the agent loop's response_format gating still works through this
// wrapper. Without this, wrapping an OpenAI provider would silently
// drop its native structured-output support.
func (p *RetryProvider) SupportsResponseFormat() bool {
	return SupportsNativeResponseFormat(p.inner)
}

// Chat retries the inner.Chat call according to RetryConfig. Circuit
// breaker is consulted before every request; opens after enough
// consecutive failed requests.
func (p *RetryProvider) Chat(ctx context.Context, req LLMRequest) (*LLMResponse, error) {
	if open, err := p.checkCircuit(); open {
		return nil, err
	}

	var lastErr error
	for attempt := 0; attempt < p.cfg.MaxAttempts; attempt++ {
		if attempt > 0 {
			if err := p.sleepBackoff(ctx, attempt); err != nil {
				return nil, err
			}
		}
		resp, err := p.inner.Chat(ctx, req)
		if err == nil {
			p.recordSuccess()
			return resp, nil
		}
		// Context cancellation always wins.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		lastErr = err
		if p.cfg.Classify(err) == Permanent {
			p.recordFailure()
			return nil, err
		}
	}
	p.recordFailure()
	return nil, fmt.Errorf("retry-provider: %d attempts failed: %w", p.cfg.MaxAttempts, lastErr)
}

// ChatStream retries ONLY before a stream successfully opens. Once the
// channel is being drained the caller already saw partial content; we
// don't restart from scratch because that would duplicate tokens.
func (p *RetryProvider) ChatStream(ctx context.Context, req LLMRequest) (<-chan StreamChunk, error) {
	if open, err := p.checkCircuit(); open {
		return nil, err
	}

	var lastErr error
	for attempt := 0; attempt < p.cfg.MaxAttempts; attempt++ {
		if attempt > 0 {
			if err := p.sleepBackoff(ctx, attempt); err != nil {
				return nil, err
			}
		}
		ch, err := p.inner.ChatStream(ctx, req)
		if err == nil {
			p.recordSuccess()
			return ch, nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		lastErr = err
		if p.cfg.Classify(err) == Permanent {
			p.recordFailure()
			return nil, err
		}
	}
	p.recordFailure()
	return nil, fmt.Errorf("retry-provider: %d attempts failed: %w", p.cfg.MaxAttempts, lastErr)
}

// sleepBackoff waits for the geometric+jittered duration before the next
// attempt. Respects ctx so a cancellation interrupts the wait.
func (p *RetryProvider) sleepBackoff(ctx context.Context, attempt int) error {
	base := float64(p.cfg.InitialBackoff)
	for i := 1; i < attempt; i++ {
		base *= p.cfg.BackoffFactor
	}
	if base > float64(p.cfg.MaxBackoff) {
		base = float64(p.cfg.MaxBackoff)
	}
	p.rngMu.Lock()
	delta := (p.rng.Float64()*2 - 1) * p.cfg.Jitter * base
	p.rngMu.Unlock()
	wait := time.Duration(base + delta)
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// checkCircuit reports whether the breaker should block this call.
// Returns true + ErrCircuitOpen when the cooldown is still active.
// When the cooldown has elapsed the breaker resets to closed and the
// caller may proceed (half-open behavior: a single subsequent success
// fully resets the counter; a failure re-opens the circuit).
func (p *RetryProvider) checkCircuit() (bool, error) {
	if p.cfg.CircuitFailureThreshold <= 0 {
		return false, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.openedAt.IsZero() {
		return false, nil
	}
	if time.Since(p.openedAt) < p.cfg.CircuitCooldown {
		return true, ErrCircuitOpen
	}
	// Cooldown elapsed — half-open, let the call through and clear the
	// trip timestamp. The consecutive-failure counter stays as-is so a
	// fresh failure trips again immediately.
	p.openedAt = time.Time{}
	return false, nil
}

// recordSuccess closes the circuit and resets the consecutive-failure
// counter. Called on any successful inner call.
func (p *RetryProvider) recordSuccess() {
	if p.cfg.CircuitFailureThreshold <= 0 {
		return
	}
	p.mu.Lock()
	p.consecFails = 0
	p.openedAt = time.Time{}
	p.mu.Unlock()
}

// recordFailure increments the consecutive-failure counter. When it
// crosses the threshold the circuit opens (openedAt = now).
func (p *RetryProvider) recordFailure() {
	if p.cfg.CircuitFailureThreshold <= 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.consecFails++
	if p.consecFails >= p.cfg.CircuitFailureThreshold {
		p.openedAt = time.Now()
	}
}

// DefaultRetryClassifier inspects the error message for common
// transient patterns. Good enough for the major provider SDKs without
// per-SDK plumbing; callers with stricter needs override Classify.
//
// Transient: HTTP 5xx, 429 rate-limit, EOF, connection refused / reset,
// i/o timeout, "no such host" (DNS blip).
// Permanent: everything else (4xx, malformed-request, model-not-found, ...).
func DefaultRetryClassifier(err error) RetryDecision {
	if err == nil {
		return Permanent
	}
	msg := strings.ToLower(err.Error())
	transientNeedles := []string{
		"500", "502", "503", "504",
		"429",
		"timeout",
		"connection refused", "connection reset", "broken pipe",
		"eof",
		"no such host",
		"i/o timeout",
		"temporarily unavailable",
		"rate limit",
	}
	for _, n := range transientNeedles {
		if strings.Contains(msg, n) {
			return Transient
		}
	}
	return Permanent
}
