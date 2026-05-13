package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/provider"
)

// marshalChoice runs applyToolChoice on a fresh request params struct and
// returns the JSON it would serialize to — that's what actually hits the
// OpenAI API on the wire.
func marshalChoice(t *testing.T, c provider.ToolChoice) string {
	t.Helper()
	params := buildToolChoiceParams(c)
	if params == nil {
		return ""
	}
	b, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func TestOpenAIToolChoice_Auto(t *testing.T) {
	got := marshalChoice(t, provider.ToolChoiceAuto())
	if !strings.Contains(got, `"auto"`) {
		t.Errorf("auto should marshal to bare \"auto\", got %s", got)
	}
}

func TestOpenAIToolChoice_Required(t *testing.T) {
	got := marshalChoice(t, provider.ToolChoiceRequired())
	if !strings.Contains(got, `"required"`) {
		t.Errorf("required should marshal to bare \"required\", got %s", got)
	}
}

func TestOpenAIToolChoice_None(t *testing.T) {
	got := marshalChoice(t, provider.ToolChoiceNone())
	if !strings.Contains(got, `"none"`) {
		t.Errorf("none should marshal to bare \"none\", got %s", got)
	}
}

func TestOpenAIToolChoice_Specific(t *testing.T) {
	got := marshalChoice(t, provider.ToolChoiceSpecific("publish_pages"))
	if !strings.Contains(got, `"function"`) || !strings.Contains(got, `"publish_pages"`) {
		t.Errorf("specific should marshal to {type:function,function:{name:publish_pages}}, got %s", got)
	}
}

func TestOpenAIToolChoice_ZeroValue_OmitsField(t *testing.T) {
	// Zero ToolChoice (Auto) — provider can either send "auto" or omit
	// the field entirely; both behaviours are equivalent for OpenAI. We
	// just assert no panic and a marshalable result.
	if got := marshalChoice(t, provider.ToolChoice{}); !strings.Contains(got, `"auto"`) {
		t.Errorf("zero-value ToolChoice should default to auto, got %s", got)
	}
}
