package looper

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/loop"
	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/pause"
	"github.com/cuatroochenta-idi/looper-agent/provider"
)

// echoProvider returns "echo: <last user input>" for every Chat /
// ChatStream invocation. It is the deterministic test fixture used by the
// concurrency stress tests so each parallel run has a unique, identifiable
// answer — that way cross-talk (run A's answer ending up in run B's
// history) is visible as a failed string match instead of a flaky bug.
type echoProvider struct {
	model string
	calls atomic.Int64
}

func (p *echoProvider) Model() string { return p.model }

func (p *echoProvider) Translator() provider.Translator { return nil }

func (p *echoProvider) Chat(_ context.Context, req provider.LLMRequest) (*provider.LLMResponse, error) {
	p.calls.Add(1)
	var last string
	for _, m := range req.Messages {
		if m.Type == message.MessageUser {
			last = m.Content
		}
	}
	return &provider.LLMResponse{
		Content: "echo: " + last,
		IsFinal: true,
		Usage:   provider.Usage{InputTokens: 1, OutputTokens: 1},
	}, nil
}

func (p *echoProvider) ChatStream(ctx context.Context, req provider.LLMRequest) (<-chan provider.StreamChunk, error) {
	ch := make(chan provider.StreamChunk, 1)
	go func() {
		defer close(ch)
		resp, _ := p.Chat(ctx, req)
		ch <- provider.StreamChunk{
			Content: resp.Content,
			IsFinal: true,
			Usage:   &resp.Usage,
		}
	}()
	return ch, nil
}

// TestConcurrent_AgentRun_NoCrosstalk asserts that running N goroutines on
// the same Agent instance produces N independent results — each goroutine's
// output is keyed by its own input, never another goroutine's. Run with
// `go test -race` to catch shared-state writes inside the framework.
func TestConcurrent_AgentRun_NoCrosstalk(t *testing.T) {
	const N = 50

	prov := &echoProvider{model: "echo"}
	agent := MustNewAgent(prov, "you are an echo bot")

	var wg sync.WaitGroup
	type result struct {
		i      int
		input  string
		output string
		userN  int
	}
	results := make(chan result, N)

	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			in := fmt.Sprintf("ping-%d", idx)
			res, err := agent.Run(context.Background(), in)
			if err != nil {
				t.Errorf("run %d: %v", idx, err)
				return
			}
			userMsgs := 0
			for _, m := range res.History.Messages() {
				if m.Type == message.MessageUser {
					userMsgs++
				}
			}
			results <- result{i: idx, input: in, output: res.Output, userN: userMsgs}
		}(i)
	}
	wg.Wait()
	close(results)

	got := 0
	for r := range results {
		got++
		wantSubstring := r.input
		if !strings.Contains(r.output, wantSubstring) {
			t.Errorf("run %d cross-talk: input=%q output=%q (expected output to contain %q)",
				r.i, r.input, r.output, wantSubstring)
		}
		if r.userN != 1 {
			t.Errorf("run %d: expected exactly 1 user message in history, got %d", r.i, r.userN)
		}
	}
	if got != N {
		t.Errorf("expected %d results, got %d", N, got)
	}
	if int64(N) != prov.calls.Load() {
		t.Errorf("expected %d provider Chat calls, got %d", N, prov.calls.Load())
	}
}

// TestConcurrent_AgentRun_WithStatefulValidator asserts that registering a
// validator on a shared Agent does NOT introduce cross-run state
// corruption. The validator's per-call counter is wrapped in a mutex; the
// per-run failure budget is tracked on the Iterator (per-run state), so
// under -race no writes should be flagged against the agent itself.
func TestConcurrent_AgentRun_WithStatefulValidator(t *testing.T) {
	const N = 30
	prov := &echoProvider{model: "echo"}

	var (
		validatorMu sync.Mutex
		seenInputs  = map[string]int{}
	)

	agent := MustNewAgent(prov, "be concise",
		WithTurnValidatorFunc(func(snap loop.TurnSnapshot) loop.Outcome {
			validatorMu.Lock()
			// Use the assistant text as the dedupe key — every run has a
			// unique echo of its input so we can confirm the validator
			// observes each run's own answer, not a neighbour's.
			seenInputs[snap.LastAssistantContent]++
			validatorMu.Unlock()
			return loop.Outcome{OK: true}
		}, 0),
	)

	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, err := agent.Run(context.Background(), fmt.Sprintf("q-%d", idx))
			if err != nil {
				t.Errorf("run %d: %v", idx, err)
			}
		}(i)
	}
	wg.Wait()

	validatorMu.Lock()
	defer validatorMu.Unlock()
	if len(seenInputs) != N {
		t.Errorf("validator saw %d distinct answers, expected %d (cross-talk?)", len(seenInputs), N)
	}
}

// TestConcurrent_PauseManager_RoutesResumesByRequestID is the regression
// guard for the PauseManager per-request channel fix. Two concurrent
// pauses each set their own RequestID; two Resume calls addressed by
// RequestID land in the matching goroutines without cross-talk.
func TestConcurrent_PauseManager_RoutesResumesByRequestID(t *testing.T) {
	pm := pause.NewPauseManager()

	var wg sync.WaitGroup
	gotA := make(chan *pause.PauseResponse, 1)
	gotB := make(chan *pause.PauseResponse, 1)

	wg.Add(2)
	go func() {
		defer wg.Done()
		r, _ := pm.Pause(context.Background(), pause.PauseRequest{
			RequestID: "alpha", ToolName: "any",
		})
		gotA <- r
	}()
	go func() {
		defer wg.Done()
		r, _ := pm.Pause(context.Background(), pause.PauseRequest{
			RequestID: "beta", ToolName: "any",
		})
		gotB <- r
	}()

	// Give the goroutines time to register their channels before resuming.
	for {
		if pm.PendingCount() >= 2 {
			break
		}
	}

	if err := pm.Resume(&pause.PauseResponse{RequestID: "alpha", Action: "ok"}); err != nil {
		t.Fatalf("resume alpha: %v", err)
	}
	if err := pm.Resume(&pause.PauseResponse{RequestID: "beta", Action: "cancel"}); err != nil {
		t.Fatalf("resume beta: %v", err)
	}
	wg.Wait()
	if r := <-gotA; r.Action != "ok" {
		t.Errorf("alpha expected ok, got %+v", r)
	}
	if r := <-gotB; r.Action != "cancel" {
		t.Errorf("beta expected cancel, got %+v", r)
	}
}
