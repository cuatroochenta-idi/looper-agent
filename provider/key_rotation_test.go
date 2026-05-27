package provider

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestKeyRotation_FirstKeySuccess(t *testing.T) {
	p1 := &stubFailoverProvider{name: "k1"}
	p2 := &stubFailoverProvider{name: "k2"}
	k, err := NewKeyRotation([]LLMProvider{p1, p2}, 0)
	if err != nil {
		t.Fatalf("NewKeyRotation: %v", err)
	}
	resp, err := k.Chat(context.Background(), LLMRequest{})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "k1" {
		t.Errorf("Content = %q, want k1", resp.Content)
	}
	if p2.chatCalls.Load() != 0 {
		t.Errorf("second key called unexpectedly")
	}
}

func TestKeyRotation_SwitchOnFailure(t *testing.T) {
	p1 := &stubFailoverProvider{name: "k1", chatErr: errors.New("503 service unavailable")}
	p2 := &stubFailoverProvider{name: "k2"}
	k, _ := NewKeyRotation([]LLMProvider{p1, p2}, 0)

	resp, err := k.Chat(context.Background(), LLMRequest{})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "k2" {
		t.Errorf("Content = %q, want k2 (after switch)", resp.Content)
	}
}

func TestKeyRotation_AllKeysFailWrapsSentinel(t *testing.T) {
	p1 := &stubFailoverProvider{name: "k1", chatErr: errors.New("503")}
	p2 := &stubFailoverProvider{name: "k2", chatErr: errors.New("429")}
	k, _ := NewKeyRotation([]LLMProvider{p1, p2}, 0)

	_, err := k.Chat(context.Background(), LLMRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrAllKeysFailed) {
		t.Errorf("err = %v, want errors.Is(err, ErrAllKeysFailed)", err)
	}
}

func TestKeyRotation_RetryDelayHonoured(t *testing.T) {
	p1 := &stubFailoverProvider{name: "k1", chatErr: errors.New("503")}
	p2 := &stubFailoverProvider{name: "k2"}
	delay := 25 * time.Millisecond
	k, _ := NewKeyRotation([]LLMProvider{p1, p2}, delay)

	start := time.Now()
	_, err := k.Chat(context.Background(), LLMRequest{})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if elapsed < delay {
		t.Errorf("elapsed %s < delay %s — retry delay not enforced", elapsed, delay)
	}
}

func TestKeyRotation_ContextCancelInterruptsDelay(t *testing.T) {
	p1 := &stubFailoverProvider{name: "k1", chatErr: errors.New("503")}
	p2 := &stubFailoverProvider{name: "k2"}
	k, _ := NewKeyRotation([]LLMProvider{p1, p2}, 5*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := k.Chat(ctx, LLMRequest{})
	elapsed := time.Since(start)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if elapsed > 1*time.Second {
		t.Errorf("elapsed %s — context cancel did not break the retry delay", elapsed)
	}
}

func TestKeyRotation_RotationDelayLegacy(t *testing.T) {
	// With no WithKeyRotationMaxDelay option, the wait before every key
	// is the same retryDelay — the pre-existing fixed-interval behavior.
	inners := []LLMProvider{
		&stubFailoverProvider{name: "k1", chatErr: errors.New("err")},
		&stubFailoverProvider{name: "k2", chatErr: errors.New("err")},
		&stubFailoverProvider{name: "k3", chatErr: errors.New("err")},
	}
	k, _ := NewKeyRotation(inners, 200*time.Millisecond)
	if d := k.rotationDelay(0); d != 0 {
		t.Errorf("rotationDelay(0) = %s, want 0", d)
	}
	for i := 1; i <= 3; i++ {
		if d := k.rotationDelay(i); d != 200*time.Millisecond {
			t.Errorf("rotationDelay(%d) = %s, want fixed 200ms", i, d)
		}
	}
}

func TestKeyRotation_RotationDelayGeometric(t *testing.T) {
	// With a max delay, each successive key wait doubles up to the cap.
	// Sequence for initial=1s, max=10s: 1s, 2s, 4s, 8s, 10s, 10s, …
	inners := []LLMProvider{
		&stubFailoverProvider{name: "k1"},
		&stubFailoverProvider{name: "k2"},
		&stubFailoverProvider{name: "k3"},
		&stubFailoverProvider{name: "k4"},
		&stubFailoverProvider{name: "k5"},
		&stubFailoverProvider{name: "k6"},
	}
	k, _ := NewKeyRotation(inners, time.Second, WithKeyRotationMaxDelay(10*time.Second))
	want := []time.Duration{
		0, // i=0 → no wait
		time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		10 * time.Second, // would be 16s, capped at 10s
	}
	for i, w := range want {
		if d := k.rotationDelay(i); d != w {
			t.Errorf("rotationDelay(%d) = %s, want %s", i, d, w)
		}
	}
}

func TestKeyRotation_MaxDelayBelowInitialIsLegacy(t *testing.T) {
	// max <= initial degrades to the fixed-interval behavior so callers
	// that misconfigure WithKeyRotationMaxDelay (e.g. typo with smaller
	// duration) don't silently get unexpected backoff.
	inners := []LLMProvider{
		&stubFailoverProvider{name: "k1"},
		&stubFailoverProvider{name: "k2"},
		&stubFailoverProvider{name: "k3"},
	}
	k, _ := NewKeyRotation(inners, 500*time.Millisecond, WithKeyRotationMaxDelay(100*time.Millisecond))
	if d := k.rotationDelay(2); d != 500*time.Millisecond {
		t.Errorf("rotationDelay(2) with bad max = %s, want fixed 500ms", d)
	}
}

func TestKeyRotation_LabelOverrideAppearsInError(t *testing.T) {
	p := &stubFailoverProvider{name: "k1", chatErr: errors.New("503")}
	k, _ := NewKeyRotation([]LLMProvider{p}, 0, WithKeyRotationLabel("gemini-pool"))

	_, err := k.Chat(context.Background(), LLMRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrAllKeysFailed) {
		t.Errorf("err missing ErrAllKeysFailed: %v", err)
	}
	if msg := err.Error(); !contains(msg, "gemini-pool") {
		t.Errorf("error message %q does not include the label", msg)
	}
}

func TestKeyRotation_ChatStreamSwitchesOnOpenError(t *testing.T) {
	p1 := &stubFailoverProvider{name: "k1", streamErr: errors.New("connection reset")}
	p2 := &stubFailoverProvider{name: "k2"}
	k, _ := NewKeyRotation([]LLMProvider{p1, p2}, 0)

	ch, err := k.ChatStream(context.Background(), LLMRequest{})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	chunk := <-ch
	if chunk.Content != "k2" {
		t.Errorf("first chunk = %q, want k2", chunk.Content)
	}
}

func TestKeyRotation_ComposesWithFailover(t *testing.T) {
	// Two slots, the first with two failing keys. The outer Failover
	// returns an error that satisfies BOTH sentinels.
	slot1k1 := &stubFailoverProvider{name: "s1k1", chatErr: errors.New("503")}
	slot1k2 := &stubFailoverProvider{name: "s1k2", chatErr: errors.New("503")}
	slot2 := &stubFailoverProvider{name: "s2", chatErr: errors.New("500")}

	rot, _ := NewKeyRotation([]LLMProvider{slot1k1, slot1k2}, 0, WithKeyRotationLabel("slot1"))
	fb, _ := NewFailover(
		[]LLMProvider{rot, slot2},
		WithFailoverNames([]string{"slot1", "slot2"}),
	)

	_, err := fb.Chat(context.Background(), LLMRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrAllProvidersFailed) {
		t.Errorf("err missing ErrAllProvidersFailed: %v", err)
	}
}

func TestNewKeyRotation_RejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		fn   func() error
	}{
		{
			"empty-inners",
			func() error { _, err := NewKeyRotation(nil, 0); return err },
		},
		{
			"nil-inner",
			func() error { _, err := NewKeyRotation([]LLMProvider{nil}, 0); return err },
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

// contains is a tiny strings.Contains helper kept local to avoid pulling
// strings into the test file (every other test file in this package
// keeps the import surface minimal).
func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
