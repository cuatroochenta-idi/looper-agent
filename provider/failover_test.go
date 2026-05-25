package provider

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
)

// stubFailoverProvider is a scriptable LLMProvider used by FailoverProvider
// and KeyRotationProvider tests. Keeps the package free of network deps.
type stubFailoverProvider struct {
	name           string
	chatErr        error
	streamErr      error
	chatCalls      atomic.Int32
	streamCalls    atomic.Int32
	supportsFormat bool
}

func (s *stubFailoverProvider) Model() string          { return s.name + "-model" }
func (s *stubFailoverProvider) Translator() Translator { return nil }

func (s *stubFailoverProvider) SupportsResponseFormat() bool { return s.supportsFormat }

func (s *stubFailoverProvider) Chat(_ context.Context, _ LLMRequest) (*LLMResponse, error) {
	s.chatCalls.Add(1)
	if s.chatErr != nil {
		return nil, s.chatErr
	}
	return &LLMResponse{Content: s.name, IsFinal: true}, nil
}

func (s *stubFailoverProvider) ChatStream(_ context.Context, _ LLMRequest) (<-chan StreamChunk, error) {
	s.streamCalls.Add(1)
	if s.streamErr != nil {
		return nil, s.streamErr
	}
	ch := make(chan StreamChunk, 1)
	ch <- StreamChunk{Content: s.name, IsFinal: true}
	close(ch)
	return ch, nil
}

func TestFailoverProvider_PrimaryHealthy(t *testing.T) {
	p1 := &stubFailoverProvider{name: "openai"}
	p2 := &stubFailoverProvider{name: "google"}
	f, err := NewFailover([]LLMProvider{p1, p2})
	if err != nil {
		t.Fatalf("NewFailover: %v", err)
	}
	resp, err := f.Chat(context.Background(), LLMRequest{})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "openai" {
		t.Errorf("Content = %q, want openai", resp.Content)
	}
	if got := p2.chatCalls.Load(); got != 0 {
		t.Errorf("secondary calls = %d, want 0 (primary succeeded)", got)
	}
}

func TestFailoverProvider_SwitchesOnError(t *testing.T) {
	p1 := &stubFailoverProvider{name: "openai", chatErr: errors.New("502 bad gateway")}
	p2 := &stubFailoverProvider{name: "google"}
	f, _ := NewFailover(
		[]LLMProvider{p1, p2},
		WithFailoverNames([]string{"openai", "google"}),
	)
	resp, err := f.Chat(context.Background(), LLMRequest{})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "google" {
		t.Errorf("Content = %q, want google (after switch)", resp.Content)
	}
}

func TestFailoverProvider_AllFailWrapsSentinel(t *testing.T) {
	p1 := &stubFailoverProvider{name: "p1", chatErr: errors.New("503")}
	p2 := &stubFailoverProvider{name: "p2", chatErr: errors.New("rate limit")}
	f, _ := NewFailover([]LLMProvider{p1, p2})

	_, err := f.Chat(context.Background(), LLMRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrAllProvidersFailed) {
		t.Errorf("err = %v, want errors.Is(err, ErrAllProvidersFailed)", err)
	}
}

func TestFailoverProvider_ContextCancelShortCircuits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	p1 := &stubFailoverProvider{name: "p1"}
	p2 := &stubFailoverProvider{name: "p2"}
	f, _ := NewFailover([]LLMProvider{p1, p2})

	_, err := f.Chat(ctx, LLMRequest{})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if p1.chatCalls.Load() != 0 || p2.chatCalls.Load() != 0 {
		t.Errorf("calls: p1=%d p2=%d, want 0/0 (pre-cancelled ctx)",
			p1.chatCalls.Load(), p2.chatCalls.Load())
	}
}

func TestFailoverProvider_ContextCancelFromInnerStopsIteration(t *testing.T) {
	p1 := &stubFailoverProvider{name: "p1", chatErr: context.Canceled}
	p2 := &stubFailoverProvider{name: "p2"}
	f, _ := NewFailover([]LLMProvider{p1, p2})

	_, err := f.Chat(context.Background(), LLMRequest{})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if got := p2.chatCalls.Load(); got != 0 {
		t.Errorf("secondary calls = %d, want 0 (context-cancel from inner must not fall over)", got)
	}
}

func TestFailoverProvider_ChatStreamSwitchesOnOpenError(t *testing.T) {
	p1 := &stubFailoverProvider{name: "p1", streamErr: errors.New("connection reset")}
	p2 := &stubFailoverProvider{name: "p2"}
	f, _ := NewFailover([]LLMProvider{p1, p2})

	ch, err := f.ChatStream(context.Background(), LLMRequest{})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	chunk := <-ch
	if chunk.Content != "p2" {
		t.Errorf("first chunk = %q, want p2", chunk.Content)
	}
}

func TestFailoverProvider_SupportsResponseFormatIsAND(t *testing.T) {
	cases := []struct {
		name   string
		flags  []bool
		expect bool
	}{
		{"both-true", []bool{true, true}, true},
		{"mixed", []bool{true, false}, false},
		{"both-false", []bool{false, false}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			inners := make([]LLMProvider, len(c.flags))
			for i, v := range c.flags {
				inners[i] = &stubFailoverProvider{name: fmt.Sprintf("p%d", i), supportsFormat: v}
			}
			f, _ := NewFailover(inners)
			if got := f.SupportsResponseFormat(); got != c.expect {
				t.Errorf("SupportsResponseFormat() = %v, want %v", got, c.expect)
			}
		})
	}
}

func TestNewFailover_RejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		fn   func() error
	}{
		{
			"empty-inners",
			func() error { _, err := NewFailover(nil); return err },
		},
		{
			"nil-inner",
			func() error { _, err := NewFailover([]LLMProvider{nil}); return err },
		},
		{
			"names-length-mismatch",
			func() error {
				_, err := NewFailover(
					[]LLMProvider{&stubFailoverProvider{name: "a"}},
					WithFailoverNames([]string{"a", "b"}),
				)
				return err
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.fn(); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
