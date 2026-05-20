package google

import (
	"strings"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/provider"
	genai "google.golang.org/genai"
)

// TestFinishReasonError_HappyPaths makes sure the happy-path reasons
// stay non-errors. STOP is the normal terminator and MAX_TOKENS yields
// a usable (truncated) string — turning either into an error would
// break every Gemini response.
func TestFinishReasonError_HappyPaths(t *testing.T) {
	for _, r := range []genai.FinishReason{
		"",
		genai.FinishReasonUnspecified,
		genai.FinishReasonStop,
		genai.FinishReasonMaxTokens,
	} {
		if err := finishReasonError(r); err != nil {
			t.Errorf("finishReasonError(%q) = %v, want nil", r, err)
		}
	}
}

// TestFinishReasonError_TerminalReasons covers every Gemini reason
// that means "the turn is broken, surface the cause". The exact
// wording is asserted loosely (Contains) so future tweaks to the
// human-readable message don't break the test, but the reason code
// itself must always appear so logs are searchable.
func TestFinishReasonError_TerminalReasons(t *testing.T) {
	cases := []struct {
		reason genai.FinishReason
		want   string // substring that must appear in the error message
	}{
		// The wire-only Gemini 3.x reason not yet in SDK constants.
		// This is the canonical case for our thoughtSignature plumbing.
		{"MISSING_THOUGHT_SIGNATURE", "MISSING_THOUGHT_SIGNATURE"},
		{genai.FinishReasonMalformedFunctionCall, "MALFORMED_FUNCTION_CALL"},
		{genai.FinishReasonUnexpectedToolCall, "UNEXPECTED_TOOL_CALL"},
		{genai.FinishReasonSafety, "SAFETY"},
		{genai.FinishReasonProhibitedContent, "PROHIBITED_CONTENT"},
		{genai.FinishReasonBlocklist, "BLOCKLIST"},
		{genai.FinishReasonSPII, "SPII"},
		{genai.FinishReasonRecitation, "RECITATION"},
		{genai.FinishReasonLanguage, "LANGUAGE"},
		{genai.FinishReasonImageSafety, "IMAGE_SAFETY"},
		{genai.FinishReasonImageProhibitedContent, "IMAGE_PROHIBITED_CONTENT"},
		{genai.FinishReasonImageRecitation, "IMAGE_RECITATION"},
		{genai.FinishReasonImageOther, "IMAGE_OTHER"},
		{genai.FinishReasonNoImage, "NO_IMAGE"},
		{genai.FinishReasonOther, "OTHER"},
	}
	for _, tc := range cases {
		err := finishReasonError(tc.reason)
		if err == nil {
			t.Errorf("finishReasonError(%q): want non-nil error", tc.reason)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("finishReasonError(%q) = %q, want substring %q",
				tc.reason, err.Error(), tc.want)
		}
	}
}

// TestFinishReasonError_UnknownReasonStillSurfaces guards against API
// drift: a new Gemini state must produce an error tagged "unrecognised"
// so the gap is visible in logs/tests rather than silently becoming a
// clean stop. If/when the SDK adds the constant we update the switch
// and this test still passes (the message just becomes more specific).
func TestFinishReasonError_UnknownReasonStillSurfaces(t *testing.T) {
	err := finishReasonError(genai.FinishReason("FUTURE_REASON_NOT_YET_KNOWN"))
	if err == nil {
		t.Fatal("unknown reason must produce an error")
	}
	if !strings.Contains(err.Error(), "FUTURE_REASON_NOT_YET_KNOWN") {
		t.Errorf("unknown reason error should mention the value: %q", err)
	}
	if !strings.Contains(err.Error(), "unrecognised") {
		t.Errorf("unknown reason error should flag itself as unrecognised: %q", err)
	}
}

// TestDedupe checks the dedupe helper used by logDroppedParts so a
// response with N inlineData parts only emits one entry per kind.
func TestDedupe(t *testing.T) {
	got := dedupe([]string{"inlineData", "fileData", "inlineData", "inlineData", "fileData"})
	want := []string{"inlineData", "fileData"}
	if len(got) != len(want) {
		t.Fatalf("dedupe result length = %d, want %d (got %v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("dedupe[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

// TestFromNative_SurfacesTerminalFinishReason exercises the wiring in
// FromNative so a candidate that stopped for a terminal reason returns
// (partial result, typed error). The agent loop needs both: the error
// to know something went wrong, and the partial content so anything
// the model said before the cut is preserved.
func TestFromNative_SurfacesTerminalFinishReason(t *testing.T) {
	tr := &Translator{model: "gemini-3.5-flash"}
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			Content: &genai.Content{
				Role:  "model",
				Parts: []*genai.Part{{Text: "partial answer before cut"}},
			},
			FinishReason: "MISSING_THOUGHT_SIGNATURE",
		}},
	}
	got, err := tr.FromNative(resp)
	if err == nil {
		t.Fatal("FromNative must return an error for MISSING_THOUGHT_SIGNATURE")
	}
	if !strings.Contains(err.Error(), "MISSING_THOUGHT_SIGNATURE") {
		t.Errorf("error must mention the reason: %v", err)
	}
	if got == nil || got.Content != "partial answer before cut" {
		t.Errorf("partial content lost: got %+v", got)
	}
}

// TestFromNative_HappyPathIsErrorFree confirms STOP doesn't trip the
// new error path. This is the dominant case, so a regression here
// would break every Gemini call.
func TestFromNative_HappyPathIsErrorFree(t *testing.T) {
	tr := &Translator{model: "gemini-3.5-flash"}
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			Content: &genai.Content{
				Role:  "model",
				Parts: []*genai.Part{{Text: "done"}},
			},
			FinishReason: genai.FinishReasonStop,
		}},
	}
	got, err := tr.FromNative(resp)
	if err != nil {
		t.Fatalf("STOP must not produce an error, got %v", err)
	}
	if got.Content != "done" || !got.IsFinal {
		t.Errorf("expected IsFinal=true with content 'done', got %+v", got)
	}
}

// TestStream_SurfacesTerminalFinishReason exercises the streaming
// wiring: when the final chunk arrives with a terminal reason, the
// final StreamChunk must carry Error so the loop propagates it.
func TestStream_SurfacesTerminalFinishReason(t *testing.T) {
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			Content: &genai.Content{
				Role:  "model",
				Parts: []*genai.Part{{Text: "partial"}},
			},
			FinishReason: genai.FinishReasonMalformedFunctionCall,
		}},
	}
	ch := make(chan provider.StreamChunk, 8)
	go func() {
		defer close(ch)
		processStream(fakeSeq([]*genai.GenerateContentResponse{resp}), ch, false)
	}()
	chunks := drainStream(ch)

	var final provider.StreamChunk
	for _, c := range chunks {
		if c.IsFinal {
			final = c
		}
	}
	if final.Error == nil {
		t.Fatalf("final chunk must carry the terminal-reason error; chunks=%+v", chunks)
	}
	if !strings.Contains(final.Error.Error(), "MALFORMED_FUNCTION_CALL") {
		t.Errorf("final chunk error must mention the reason: %v", final.Error)
	}
}

// TestStream_HappyPathLeavesErrorNil guards the STOP path: the final
// chunk must have Error=nil so consumers don't treat a normal stop as
// a failure.
func TestStream_HappyPathLeavesErrorNil(t *testing.T) {
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			Content: &genai.Content{
				Role:  "model",
				Parts: []*genai.Part{{Text: "ok"}},
			},
			FinishReason: genai.FinishReasonStop,
		}},
	}
	ch := make(chan provider.StreamChunk, 8)
	go func() {
		defer close(ch)
		processStream(fakeSeq([]*genai.GenerateContentResponse{resp}), ch, false)
	}()
	chunks := drainStream(ch)
	for _, c := range chunks {
		if c.IsFinal && c.Error != nil {
			t.Fatalf("STOP must not set Error on the final chunk, got %v", c.Error)
		}
	}
}
