package looper

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/provider"
	"github.com/cuatroochenta-idi/looper-agent/provider/anthropic"
	"github.com/cuatroochenta-idi/looper-agent/provider/google"
	"github.com/cuatroochenta-idi/looper-agent/provider/openai"
	"github.com/cuatroochenta-idi/looper-agent/tool"
)

// compatToolInput exercises the schema features that broke in real production
// payloads: a stdlib type (time.Time), a slice of nested structs, a required
// scalar, and an optional flag. If any translator regresses on schemas that
// include these, this test catches it before the request hits the wire.
type compatToolInput struct {
	Query  string         `json:"query" jsonschema:"description=Search query,required"`
	Limit  int            `json:"limit" jsonschema:"description=Result cap,minimum=1,maximum=100"`
	Filter compatFilter   `json:"filter,omitempty" jsonschema:"description=Optional filter"`
	Items  []compatItem   `json:"items,omitempty" jsonschema:"description=Bulk inputs"`
	After  time.Time      `json:"after,omitempty" jsonschema:"description=Cutoff timestamp"`
}

type compatFilter struct {
	Status string `json:"status" jsonschema:"enum=active|paused|archived"`
}

type compatItem struct {
	ID    string `json:"id" jsonschema:"required"`
	Score float64 `json:"score" jsonschema:"minimum=0,maximum=1"`
}

// newCompatTool builds the same tool used by every provider sub-test below
// so the schema input is identical across providers.
func newCompatTool(t *testing.T) *tool.Tool {
	t.Helper()
	tl, err := tool.NewTool(compatToolInput{},
		func(ctx context.Context, in compatToolInput) (string, error) {
			return "ok", nil
		},
		tool.ToolConfig{
			Name:        "compat_probe",
			Description: "Schema-feature probe used by provider compatibility tests.",
		},
	)
	if err != nil {
		t.Fatalf("compat tool build: %v", err)
	}
	return tl
}

// requireJSONMarshal asserts the value round-trips through encoding/json
// without error and that the resulting JSON mentions the marker key. If a
// translator silently drops fields it would otherwise be hard to notice.
func requireJSONMarshal(t *testing.T, label string, v any, mustContain ...string) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("%s: json.Marshal: %v", label, err)
	}
	for _, marker := range mustContain {
		if !strings.Contains(string(b), marker) {
			t.Errorf("%s: payload missing %q\npayload=%s", label, marker, string(b))
		}
	}
}

func TestProviderCompat_OpenAI_ToolSchemaShape(t *testing.T) {
	p := openai.NewProvider("test-key-not-used")
	tl := newCompatTool(t)

	native := p.Translator().ToNative("you are a probe", nil, []*tool.Tool{tl})
	// The OpenAI translator returns its private native struct as `any`; the
	// only stable assertion we can make without poking at unexported fields
	// is "this thing marshals to JSON". That's enough to catch a panic /
	// nil map / cycle introduced by a schema refactor.
	requireJSONMarshal(t, "openai.ToNative", native)
}

func TestProviderCompat_Anthropic_ToolSchemaShape(t *testing.T) {
	p := anthropic.NewProvider("test-key-not-used")
	tl := newCompatTool(t)

	native := p.Translator().ToNative("you are a probe", nil, []*tool.Tool{tl})
	requireJSONMarshal(t, "anthropic.ToNative", native)
}

func TestProviderCompat_Google_ToolSchemaShape(t *testing.T) {
	// Google's constructor pings the SDK at boot when given a real key; with
	// a fake key it just stores the credentials, so this is still safe.
	p := google.NewProvider("test-key-not-used")
	tl := newCompatTool(t)

	native := p.Translator().ToNative("you are a probe", nil, []*tool.Tool{tl})
	requireJSONMarshal(t, "google.ToNative", native)
}

// TestSchemaMap_StrictModeShape asserts the public Tool.SchemaMap() that
// every provider feeds to its SDK carries the strict-mode invariants we just
// added: additionalProperties:false on the root object, items schema on the
// slice property, format=date-time on the timestamp, and the required list.
func TestSchemaMap_StrictModeShape(t *testing.T) {
	tl := newCompatTool(t)
	m := tl.SchemaMap()

	if ap, ok := m["additionalProperties"].(bool); !ok || ap {
		t.Errorf("root additionalProperties should be false, got %v", m["additionalProperties"])
	}
	req, ok := m["required"].([]any)
	if !ok {
		t.Fatalf("root.required missing, got %T", m["required"])
	}
	if len(req) == 0 || req[0] != "query" {
		t.Errorf("expected 'query' in required, got %v", req)
	}

	props := m["properties"].(map[string]any)
	after := props["after"].(map[string]any)
	if after["format"] != "date-time" {
		t.Errorf("after.format should be date-time, got %v", after["format"])
	}

	items := props["items"].(map[string]any)
	inner, ok := items["items"].(map[string]any)
	if !ok {
		t.Fatalf("items.items schema missing: %v", items)
	}
	if inner["additionalProperties"] != false {
		t.Errorf("nested compatItem schema should also be strict, got %v", inner["additionalProperties"])
	}

	// Sanity: avoid unused imports if message ever stops being needed.
	_ = message.NewUserMessage
	_ = provider.ReasoningEffortNone
}
