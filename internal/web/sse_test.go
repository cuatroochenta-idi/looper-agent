package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type sseFrame struct{ event, data string }

// readFrames parses SSE frames from body until ctx is cancelled or body closes.
// Comment lines (heartbeats / the connect prelude) are skipped.
func readFrames(ctx context.Context, body *bufio.Reader, out chan<- sseFrame) {
	var ev, data string
	for {
		line, err := body.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			if ev != "" || data != "" {
				select {
				case out <- sseFrame{ev, data}:
				case <-ctx.Done():
					return
				}
			}
			ev, data = "", ""
		case strings.HasPrefix(line, ":"):
			// comment / heartbeat / connect prelude
		case strings.HasPrefix(line, "event: "):
			ev = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data = strings.TrimPrefix(line, "data: ")
		}
	}
}

// readPrelude reads the first line of the stream and asserts it is a comment —
// the keep-alive framing the heartbeat also uses.
func readPrelude(t *testing.T, br *bufio.Reader) {
	t.Helper()
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read prelude: %v", err)
	}
	if !strings.HasPrefix(line, ":") {
		t.Fatalf("expected a comment prelude, got %q", line)
	}
}

func TestSSERunsTopicReceivesTypedEvents(t *testing.T) {
	srv, _ := NewServer()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/events?topics=runs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	readPrelude(t, br) // subscription is registered by the time headers arrive

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	frames := make(chan sseFrame, 32)
	go readFrames(ctx, br, frames)

	// A run_start on the ingest path publishes run_updated (to runs) then
	// runs_changed (to runs).
	ingestEvent(t, srv.Handler(), "run_start", "r1", nil, runStartPayload{Input: "hello"})

	var sawChanged, sawUpdated bool
	deadline := time.After(3 * time.Second)
	for !(sawChanged && sawUpdated) {
		select {
		case f := <-frames:
			switch f.event {
			case "runs_changed":
				sawChanged = true
			case "run_updated":
				sawUpdated = true
				if !strings.Contains(f.data, `"id":"r1"`) {
					t.Errorf("run_updated missing id: %s", f.data)
				}
			}
		case <-deadline:
			t.Fatalf("missing events: runs_changed=%v run_updated=%v", sawChanged, sawUpdated)
		}
	}
}

func TestSSEChunkLiveOnlyNotPersisted(t *testing.T) {
	srv, _ := NewServer()
	release := make(chan struct{})
	srv.SetRunner(func(ctx context.Context, input string) (<-chan StepEvent, <-chan RunSummary, error) {
		steps := make(chan StepEvent, 8)
		summary := make(chan RunSummary, 1)
		go func() {
			<-release // hold until the SSE client has subscribed
			steps <- StepEvent{Kind: StepKindLLMCall, Turn: 0}
			steps <- StepEvent{Kind: StepKindStreamingChunk, Turn: 0, Content: "hello "}
			steps <- StepEvent{Kind: StepKindStreamingChunk, Turn: 0, Content: "world"}
			steps <- StepEvent{Kind: StepKindFinal, Turn: 0, Content: "hello world"}
			close(steps)
			summary <- RunSummary{Status: "completed", Turns: 1, Output: "hello world", TotalUSD: 0.01, InputTokens: 10, OutputTokens: 5}
			close(summary)
		}()
		return steps, summary, nil
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	id := postRun(t, ts.URL, "hello")

	resp, err := http.Get(ts.URL + "/api/events?topics=run:" + id)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	readPrelude(t, br)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	frames := make(chan sseFrame, 64)
	go readFrames(ctx, br, frames)

	close(release) // runner may now stream

	var sawChunk, sawStep, sawUpdated bool
	deadline := time.After(3 * time.Second)
	for !(sawChunk && sawStep && sawUpdated) {
		select {
		case f := <-frames:
			switch f.event {
			case "chunk":
				sawChunk = true
				if !strings.Contains(f.data, "hello") || !strings.Contains(f.data, `"kind":"text"`) {
					t.Errorf("chunk payload wrong: %s", f.data)
				}
			case "step_appended":
				sawStep = true
			case "run_updated":
				sawUpdated = true
			}
		case <-deadline:
			t.Fatalf("missing events: chunk=%v step=%v updated=%v", sawChunk, sawStep, sawUpdated)
		}
	}

	// Wait for the run to finalize, then confirm chunks were never persisted:
	// the final text is stored, but no streaming chunk survives in the detail.
	var d RunDetail
	for i := 0; i < 50; i++ {
		getJSONURL(t, ts.URL+"/api/state/runs/"+id, &d)
		if d.Status == "completed" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if d.Status != "completed" {
		t.Fatalf("run did not complete: status %q", d.Status)
	}
	if len(d.TurnsDetail) == 0 || d.TurnsDetail[0].Final != "hello world" {
		t.Fatalf("final response not persisted: %+v", d.TurnsDetail)
	}
	// Chunks are live-only: they never entered the store, so the persisted
	// turn carries no assistant_text accumulated from streaming deltas.
	if d.TurnsDetail[0].AssistantText != "" {
		t.Errorf("streaming chunk leaked into persisted state: %q", d.TurnsDetail[0].AssistantText)
	}
}

func postRun(t *testing.T, base, input string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"input": input})
	resp, err := http.Post(base+"/api/run", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/run: status %d", resp.StatusCode)
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode run id: %v", err)
	}
	if out.ID == "" {
		t.Fatal("empty run id")
	}
	return out.ID
}

func getJSONURL(t *testing.T, url string, out any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatalf("GET %s: decode: %v", url, err)
	}
}
