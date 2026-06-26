package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/openai/openai-go"
)

// When no temperature is configured (the zero value), the request must omit
// the temperature field entirely so the provider's own default applies. This
// is what lets reasoning models (gpt-5.x, o-series) work — they reject any
// explicit temperature other than 1.
func TestTranslatorOmitsTemperatureWhenUnset(t *testing.T) {
	tr := &Translator{model: "gpt-5.5"} // temperature 0 == unset

	out := tr.ToNative("you are a test", nil, nil).(openai.ChatCompletionNewParams)
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "temperature") {
		t.Errorf("temperature must be omitted when unset, got: %s", b)
	}
}

// A configured temperature is still sent verbatim.
func TestTranslatorSendsTemperatureWhenSet(t *testing.T) {
	tr := &Translator{model: "gpt-4o", temperature: 0.7}

	out := tr.ToNative("you are a test", nil, nil).(openai.ChatCompletionNewParams)
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"temperature":0.7`) {
		t.Errorf("configured temperature 0.7 must be sent, got: %s", b)
	}
}
