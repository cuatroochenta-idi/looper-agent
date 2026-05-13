package openai

import "testing"

func TestExtractReasoningField(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"empty", "", ""},
		{"plain string", `{"reasoning_content":"thinking out loud"}`, "thinking out loud"},
		{"alias", `{"reasoning":"alt key"}`, "alt key"},
		{"nested text", `{"reasoning_content":{"text":"deep"}}`, "deep"},
		{"absent", `{"content":"hello"}`, ""},
		{"invalid json", `not json`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := extractReasoningField(c.raw); got != c.want {
				t.Fatalf("extractReasoningField(%q) = %q; want %q", c.raw, got, c.want)
			}
		})
	}
}

func TestHarmonyFilter_PassesThroughWhenNoMarkers(t *testing.T) {
	h := newHarmonyFilter(true)
	c, r := h.feed("plain text without channels")
	if c != "plain text without channels" || r != "" {
		t.Fatalf("got content=%q reasoning=%q", c, r)
	}
}

func TestHarmonyFilter_SplitsAnalysisAndFinal(t *testing.T) {
	h := newHarmonyFilter(true)
	// Single-shot feed with both channels.
	c, r := h.feed("<|channel|>analysis<|message|>thinking step<|end|><|channel|>final<|message|>visible answer<|end|>")
	if c != "visible answer" {
		t.Fatalf("content=%q want %q", c, "visible answer")
	}
	if r != "thinking step" {
		t.Fatalf("reasoning=%q want %q", r, "thinking step")
	}
}

func TestHarmonyFilter_DropsReasoningWhenNotRequested(t *testing.T) {
	h := newHarmonyFilter(false)
	c, r := h.feed("<|channel|>analysis<|message|>secret<|end|><|channel|>final<|message|>OK<|end|>")
	if c != "OK" || r != "" {
		t.Fatalf("got content=%q reasoning=%q", c, r)
	}
}

func TestHarmonyFilter_HandlesPartialMarkerAcrossFeeds(t *testing.T) {
	h := newHarmonyFilter(true)
	// Mid-marker split: the buffer ends in the middle of "<|channel|>".
	c1, r1 := h.feed("hello <|chan")
	c2, r2 := h.feed("nel|>analysis<|message|>buried")
	c3, r3 := h.feed("<|end|>")
	if c1+c2+c3 != "hello " {
		t.Fatalf("content concat=%q want %q", c1+c2+c3, "hello ")
	}
	if r1+r2+r3 != "buried" {
		t.Fatalf("reasoning concat=%q want %q", r1+r2+r3, "buried")
	}
}

func TestHarmonyFilter_AcceptsThoughtAlias(t *testing.T) {
	h := newHarmonyFilter(true)
	c, r := h.feed("<|channel|>thought<|message|>aside<|end|>visible")
	if c != "visible" {
		t.Fatalf("content=%q", c)
	}
	if r != "aside" {
		t.Fatalf("reasoning=%q", r)
	}
}
