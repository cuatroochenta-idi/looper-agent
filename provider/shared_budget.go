package provider

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// SharedBudgetLimits bounds work across agents, retries and direct provider
// calls. Zero values mean unlimited. Token limits use reported usage, including
// cached input once; already in-flight requests can overshoot a token limit.
type SharedBudgetLimits struct {
	MaxRequests    int
	MaxTotalTokens int
}

type SharedBudgetSnapshot struct {
	Requests int
	Usage    Usage
}

// SharedBudget is scoped by its caller, normally to one user-requested build.
// It is safe to inherit through contexts used by parallel child agents. It does
// not reset when a Looper iterator restarts. Requests count attempted calls at
// the wrapping boundary, not hidden retries inside an upstream SDK.
type SharedBudget struct {
	mu     sync.Mutex
	limits SharedBudgetLimits
	state  SharedBudgetSnapshot
}

func NewSharedBudget(limits SharedBudgetLimits) *SharedBudget { return &SharedBudget{limits: limits} }

const (
	sharedRequestLimit = "max_requests"
	sharedTokenLimit   = "max_total_tokens"
)

// SharedBudgetExceededError is terminal: retrying with the same budget cannot
// restore capacity. Consumers can inspect it with errors.As through wrappers.
type SharedBudgetExceededError struct {
	Limit         string
	Maximum, Used int
}

func (e *SharedBudgetExceededError) Error() string {
	return fmt.Sprintf("shared usage budget exceeded: %s (%d/%d)", e.Limit, e.Used, e.Maximum)
}
func (e *SharedBudgetExceededError) ErrorCode() string { return "usage_exceeded" }

func (b *SharedBudget) exceededTokens() error {
	total := b.state.Usage.InputTokens + b.state.Usage.OutputTokens
	if b.limits.MaxTotalTokens > 0 && total >= b.limits.MaxTotalTokens {
		return &SharedBudgetExceededError{Limit: sharedTokenLimit, Maximum: b.limits.MaxTotalTokens, Used: total}
	}
	return nil
}

// BeginRequest atomically reserves one request before contacting a provider.
// Direct HTTP consumers can pair this with RecordUsage without an LLMProvider.
func (b *SharedBudget) BeginRequest() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.exceededTokens(); err != nil {
		return err
	}
	if b.limits.MaxRequests > 0 && b.state.Requests >= b.limits.MaxRequests {
		return &SharedBudgetExceededError{Limit: sharedRequestLimit, Maximum: b.limits.MaxRequests, Used: b.state.Requests}
	}
	b.state.Requests++
	return nil
}

// RecordUsage accounts one provider usage report, even for a failed request.
// Reports are increments, matching the StreamChunk usage contract. The caller
// must not submit the same report both here and through NewBudgetProvider.
func (b *SharedBudget) RecordUsage(u Usage) error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.state.Usage.InputTokens += max(0, u.InputTokens)
	b.state.Usage.OutputTokens += max(0, u.OutputTokens)
	b.state.Usage.CachedTokens += max(0, u.CachedTokens)
	b.state.Usage.CacheWriteTokens += max(0, u.CacheWriteTokens)
	b.state.Usage.Cost += max(0, u.Cost)
	return b.exceededTokens()
}

func (b *SharedBudget) Snapshot() SharedBudgetSnapshot {
	if b == nil {
		return SharedBudgetSnapshot{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

type sharedBudgetKey struct{}
type budgetCallKey struct{}

func WithSharedBudget(ctx context.Context, b *SharedBudget) context.Context {
	return context.WithValue(ctx, sharedBudgetKey{}, b)
}
func SharedBudgetFromContext(ctx context.Context) *SharedBudget {
	b, _ := ctx.Value(sharedBudgetKey{}).(*SharedBudget)
	return b
}

type budgetProvider struct{ inner LLMProvider }

// NewBudgetProvider tracks calls when a SharedBudget is present in their
// context. Otherwise it is transparent. Wrap the provider passed to the parent,
// children and auxiliary calls; nested wrappers do not double-count a call.
func NewBudgetProvider(inner LLMProvider) LLMProvider  { return &budgetProvider{inner: inner} }
func (p *budgetProvider) Model() string                { return p.inner.Model() }
func (p *budgetProvider) Translator() Translator       { return p.inner.Translator() }
func (p *budgetProvider) SupportsResponseFormat() bool { return SupportsNativeResponseFormat(p.inner) }

func beginBudgetCall(ctx context.Context) (context.Context, *SharedBudget, error) {
	b := SharedBudgetFromContext(ctx)
	if b == nil || ctx.Value(budgetCallKey{}) == b {
		return ctx, nil, nil
	}
	if err := b.BeginRequest(); err != nil {
		return ctx, nil, err
	}
	return context.WithValue(ctx, budgetCallKey{}, b), b, nil
}

func (p *budgetProvider) Chat(ctx context.Context, req LLMRequest) (*LLMResponse, error) {
	ctx, b, err := beginBudgetCall(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := p.inner.Chat(ctx, req)
	if resp != nil {
		err = errors.Join(err, b.RecordUsage(resp.Usage))
	}
	return resp, err
}

func (p *budgetProvider) ChatStream(ctx context.Context, req LLMRequest) (<-chan StreamChunk, error) {
	ctx, b, err := beginBudgetCall(ctx)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return p.inner.ChatStream(ctx, req)
	}
	requestCtx, cancel := context.WithCancel(ctx)
	stream, err := p.inner.ChatStream(requestCtx, req)
	if err != nil {
		cancel()
		return nil, err
	}
	out := make(chan StreamChunk)
	go func() {
		defer close(out)
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case chunk, ok := <-stream:
				if !ok {
					return
				}
				if chunk.Usage != nil {
					chunk.Error = errors.Join(chunk.Error, b.RecordUsage(*chunk.Usage))
				}
				select {
				case out <- chunk:
				case <-ctx.Done():
					return
				}
				if chunk.Error != nil {
					return
				}
			}
		}
	}()
	return out, nil
}
