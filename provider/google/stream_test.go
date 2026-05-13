package google

import (
	"iter"
	"strings"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/provider"
	"google.golang.org/genai"
)

// fakeResp builds a single GenerateContentResponse with one candidate
// carrying the given parts. Used by streaming tests to drive processStream
// without hitting the real Gemini API.
func fakeResp(parts ...*genai.Part) *genai.GenerateContentResponse {
	return &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{
			{
				Content:       &genai.Content{Parts: parts, Role: "model"},
				FinishReason:  genai.FinishReasonStop,
			},
		},
	}
}

// fakeSeq adapts a slice of responses into an iter.Seq2 the way the real
// genai SDK streams them.
func fakeSeq(responses []*genai.GenerateContentResponse) iter.Seq2[*genai.GenerateContentResponse, error] {
	return func(yield func(*genai.GenerateContentResponse, error) bool) {
		for _, r := range responses {
			if !yield(r, nil) {
				return
			}
		}
	}
}

// drainStream consumes every chunk on ch and returns them. Stops when the
// channel closes or after the first IsFinal chunk to keep the test bounded.
func drainStream(ch <-chan provider.StreamChunk) []provider.StreamChunk {
	var got []provider.StreamChunk
	for c := range ch {
		got = append(got, c)
		if c.IsFinal {
			break
		}
	}
	return got
}

// TestProcessStream_ThoughtBecomesContent_WhenReasoningDisabled asserts the
// regression e2e found: thinking-capable Gemini models wrap the visible
// answer as a Part.Thought=true. Before the fix, processStream dropped it
// when includeReasoning=false and the user saw an empty response.
//
// After the fix the thought text is surfaced as Content so the answer
// reaches the agent loop.
func TestProcessStream_ThoughtBecomesContent_WhenReasoningDisabled(t *testing.T) {
	resp := fakeResp(&genai.Part{Text: "visible answer", Thought: true})
	ch := make(chan provider.StreamChunk, 8)

	go func() {
		defer close(ch)
		processStream(fakeSeq([]*genai.GenerateContentResponse{resp}), ch, false)
	}()

	chunks := drainStream(ch)
	var combined strings.Builder
	for _, c := range chunks {
		combined.WriteString(c.Content)
	}
	if !strings.Contains(combined.String(), "visible answer") {
		t.Errorf("expected thought text to be surfaced as content, got chunks=%+v", chunks)
	}
	for _, c := range chunks {
		if c.Reasoning != "" {
			t.Errorf("reasoning channel should be empty when includeReasoning=false, got Reasoning=%q", c.Reasoning)
		}
	}
}

// TestProcessStream_ThoughtBecomesReasoning_WhenReasoningEnabled asserts
// the original split-channel behavior is preserved when the caller opted
// in: thought goes to Reasoning, content stays empty.
func TestProcessStream_ThoughtBecomesReasoning_WhenReasoningEnabled(t *testing.T) {
	resp := fakeResp(&genai.Part{Text: "I am thinking…", Thought: true})
	ch := make(chan provider.StreamChunk, 8)

	go func() {
		defer close(ch)
		processStream(fakeSeq([]*genai.GenerateContentResponse{resp}), ch, true)
	}()

	chunks := drainStream(ch)
	var reasoning strings.Builder
	for _, c := range chunks {
		reasoning.WriteString(c.Reasoning)
		if c.Content != "" {
			// Allow the final synthesised chunk to carry empty content; reject
			// only when actual content piggybacks on a non-final chunk.
			if !c.IsFinal {
				t.Errorf("content channel should stay empty when reasoning is on, got %q", c.Content)
			}
		}
	}
	if !strings.Contains(reasoning.String(), "I am thinking") {
		t.Errorf("expected thought to surface as Reasoning, got chunks=%+v", chunks)
	}
}

// TestProcessStream_FinalChunkAlwaysEmitsContent asserts that even when
// Gemini's UsageMetadata never arrives mid-stream, the fallback at the end
// of processStream still ships the accumulated content to the loop.
func TestProcessStream_FinalChunkAlwaysEmitsContent(t *testing.T) {
	resp := fakeResp(&genai.Part{Text: "hello world"})
	ch := make(chan provider.StreamChunk, 8)

	go func() {
		defer close(ch)
		processStream(fakeSeq([]*genai.GenerateContentResponse{resp}), ch, false)
	}()

	chunks := drainStream(ch)
	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk")
	}
	last := chunks[len(chunks)-1]
	if !last.IsFinal {
		t.Errorf("last chunk should be IsFinal=true, got %+v", last)
	}
	// The accumulated content should reach the final chunk so loop.Iterator
	// can resolve final = chunk.Content (or its fullContent fallback).
	var combined strings.Builder
	for _, c := range chunks {
		combined.WriteString(c.Content)
	}
	if !strings.Contains(combined.String(), "hello world") {
		t.Errorf("expected accumulated content to reach the loop, got chunks=%+v", chunks)
	}
}

// respWithUsage builds a GenerateContentResponse carrying the given parts
// + UsageMetadata + an explicit FinishReason. Mirrors what the genai SDK
// emits per streamed event.
func respWithUsage(finish genai.FinishReason, inputTok, outputTok int32, parts ...*genai.Part) *genai.GenerateContentResponse {
	return &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{
			{
				Content:      &genai.Content{Parts: parts, Role: "model"},
				FinishReason: finish,
			},
		},
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:     inputTok,
			CandidatesTokenCount: outputTok,
		},
	}
}

// TestProcessStream_TwoEventsWithUsageOnBoth pins the regression e2e v2
// found: Gemini emits UsageMetadata on EVERY streamed response, not just
// the last. The old code did an early-return on the first sighting,
// dropping the second response (which carries FinishReason="STOP"). The
// loop never saw IsFinal=true and the answer was lost.
//
// After the fix the stream:
//   - emits the text content from event 1
//   - keeps reading event 2 instead of returning
//   - emits exactly one IsFinal=true chunk with the accumulated content
//     and the latest usage metadata.
func TestProcessStream_TwoEventsWithUsageOnBoth(t *testing.T) {
	r1 := respWithUsage("", 0, 8, &genai.Part{Text: "I see four colorful dice."})
	r2 := respWithUsage(genai.FinishReasonStop, 0, 8) // FinishReason set, empty text
	ch := make(chan provider.StreamChunk, 16)

	go func() {
		defer close(ch)
		processStream(fakeSeq([]*genai.GenerateContentResponse{r1, r2}), ch, false)
	}()

	chunks := drainStream(ch)

	// Exactly one chunk must be marked IsFinal=true and carry usage.
	finals := 0
	var finalUsage *provider.Usage
	for _, c := range chunks {
		if c.IsFinal {
			finals++
			finalUsage = c.Usage
		}
	}
	if finals != 1 {
		t.Errorf("expected exactly 1 IsFinal chunk, got %d (chunks=%+v)", finals, chunks)
	}
	if finalUsage == nil {
		t.Errorf("final chunk should carry usage metadata, got nil")
	}
	// The text from event 1 must reach the loop.
	var combined strings.Builder
	for _, c := range chunks {
		combined.WriteString(c.Content)
	}
	if !strings.Contains(combined.String(), "four colorful dice") {
		t.Errorf("expected text from event 1 to survive, got chunks=%+v", chunks)
	}
}

// TestProcessStream_FinishReasonStopMarksFinal asserts that the malformed
// boolean expression has been replaced — FinishReason="STOP" produces
// IsFinal=true, and FinishReason="" does not.
func TestProcessStream_FinishReasonStopMarksFinal(t *testing.T) {
	r1 := respWithUsage("", 0, 4, &genai.Part{Text: "partial"})
	r2 := respWithUsage(genai.FinishReasonStop, 0, 4)
	ch := make(chan provider.StreamChunk, 16)

	go func() {
		defer close(ch)
		processStream(fakeSeq([]*genai.GenerateContentResponse{r1, r2}), ch, false)
	}()

	chunks := drainStream(ch)
	// Find the very first IsFinal chunk. The first response had
	// FinishReason="" and should not have been final; only the second one
	// (FinishReason=STOP) should.
	firstFinalIdx := -1
	for i, c := range chunks {
		if c.IsFinal {
			firstFinalIdx = i
			break
		}
	}
	if firstFinalIdx == -1 {
		t.Fatalf("no IsFinal chunk seen: %+v", chunks)
	}
	// Sanity: there must be at least one non-final chunk carrying the
	// "partial" text before the final marker.
	sawPartial := false
	for i := 0; i < firstFinalIdx; i++ {
		if strings.Contains(chunks[i].Content, "partial") {
			sawPartial = true
		}
	}
	if !sawPartial {
		t.Errorf("expected partial content chunk before the final marker, got %+v", chunks)
	}
}
