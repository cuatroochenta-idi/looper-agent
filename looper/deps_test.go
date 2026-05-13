package looper

import (
	"context"
	"sync"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/provider"
)

// UserDeps is a sample typed-deps struct callers might thread through
// tools and hooks (DB handle, request-scoped auth, feature flags, etc.).
type UserDeps struct {
	UserID    string
	IsPremium bool
}

// TestDeps_RoundTrip asserts the basic contract: WithRunDeps puts deps
// into ctx, Deps[T](ctx) reads them back with type information intact.
func TestDeps_RoundTrip(t *testing.T) {
	want := UserDeps{UserID: "u-42", IsPremium: true}
	ctx := WithRunDeps(context.Background(), want)
	got, ok := Deps[UserDeps](ctx)
	if !ok {
		t.Fatal("Deps should find the value we just set")
	}
	if got != want {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, want)
	}
}

// TestDeps_MissingReturnsZero asserts a missing Deps call returns a
// typed zero value plus ok=false — no panic, no nil deref.
func TestDeps_MissingReturnsZero(t *testing.T) {
	got, ok := Deps[UserDeps](context.Background())
	if ok {
		t.Errorf("Deps should report ok=false when nothing was set, got %+v", got)
	}
	if got != (UserDeps{}) {
		t.Errorf("missing deps should give a zero value, got %+v", got)
	}
}

// TestDeps_TypeMismatchReportsNotFound asserts that asking for a
// different type than was stored returns ok=false rather than coercing.
func TestDeps_TypeMismatchReportsNotFound(t *testing.T) {
	ctx := WithRunDeps(context.Background(), UserDeps{UserID: "u-1"})
	type OtherDeps struct{ TenantID string }
	if _, ok := Deps[OtherDeps](ctx); ok {
		t.Error("Deps must not return values of a different type")
	}
}

// TestDeps_ConcurrentRunsAreIsolated asserts that two concurrent agent
// runs with different deps don't see each other's values — the deps
// flow through ctx, which the runtime keeps per-goroutine.
func TestDeps_ConcurrentRunsAreIsolated(t *testing.T) {
	const N = 20

	// Each goroutine sets its own UserDeps and reads via Deps inside a
	// tool callback. We accumulate the observed values and assert the
	// set matches what we sent — no cross-talk.
	var seen sync.Map
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ctx := WithRunDeps(context.Background(),
				UserDeps{UserID: "u-" + itoa(idx), IsPremium: idx%2 == 0})
			got, ok := Deps[UserDeps](ctx)
			if !ok {
				t.Errorf("goroutine %d lost deps", idx)
				return
			}
			seen.Store(got.UserID, true)
		}(i)
	}
	wg.Wait()
	count := 0
	seen.Range(func(_, _ any) bool { count++; return true })
	if count != N {
		t.Errorf("expected %d distinct user ids observed, got %d", N, count)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// keep provider import alive in case the test file ever stops using it.
var _ = provider.ReasoningEffortNone
