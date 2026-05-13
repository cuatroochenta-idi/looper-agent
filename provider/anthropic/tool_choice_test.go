package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/provider"
)

func marshalChoice(t *testing.T, c provider.ToolChoice) string {
	t.Helper()
	tc := buildToolChoiceParams(c)
	if tc == nil {
		return ""
	}
	b, err := json.Marshal(tc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func TestAnthropicToolChoice_Auto(t *testing.T) {
	got := marshalChoice(t, provider.ToolChoiceAuto())
	if !strings.Contains(got, `"type":"auto"`) {
		t.Errorf("auto should marshal to type=auto, got %s", got)
	}
}

// Anthropic uses "any" for "force the model to call SOME tool".
func TestAnthropicToolChoice_Required_MapsToAny(t *testing.T) {
	got := marshalChoice(t, provider.ToolChoiceRequired())
	if !strings.Contains(got, `"type":"any"`) {
		t.Errorf("required should map to Anthropic's any, got %s", got)
	}
}

func TestAnthropicToolChoice_None(t *testing.T) {
	got := marshalChoice(t, provider.ToolChoiceNone())
	if !strings.Contains(got, `"type":"none"`) {
		t.Errorf("none should marshal to type=none, got %s", got)
	}
}

func TestAnthropicToolChoice_Specific(t *testing.T) {
	got := marshalChoice(t, provider.ToolChoiceSpecific("publish_pages"))
	if !strings.Contains(got, `"type":"tool"`) || !strings.Contains(got, `"publish_pages"`) {
		t.Errorf("specific should marshal to {type:tool,name:publish_pages}, got %s", got)
	}
}
