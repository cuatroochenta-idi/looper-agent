package provider

import (
	"testing"
)

// TestToolChoice_Constructors asserts each constructor produces a value
// the translators can switch on by Kind, with Specific carrying the name.
func TestToolChoice_Constructors(t *testing.T) {
	cases := []struct {
		name string
		got  ToolChoice
		want ToolChoiceKind
	}{
		{"auto", ToolChoiceAuto(), ToolChoiceKindAuto},
		{"required", ToolChoiceRequired(), ToolChoiceKindRequired},
		{"none", ToolChoiceNone(), ToolChoiceKindNone},
		{"specific", ToolChoiceSpecific("my_tool"), ToolChoiceKindSpecific},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got.Kind != tc.want {
				t.Errorf("Kind: got %v, want %v", tc.got.Kind, tc.want)
			}
		})
	}
	spec := ToolChoiceSpecific("publish")
	if spec.Name != "publish" {
		t.Errorf("Specific should preserve name, got %q", spec.Name)
	}
}

// TestToolChoice_ZeroValueIsAuto pins the contract that leaving the field
// unset means "auto" — so existing callers that don't set ToolChoice keep
// the legacy behavior.
func TestToolChoice_ZeroValueIsAuto(t *testing.T) {
	var zero ToolChoice
	if zero.Kind != ToolChoiceKindAuto {
		t.Errorf("zero value should be Auto, got %v", zero.Kind)
	}
	if zero.Label() != "auto" {
		t.Errorf("zero value label should be 'auto', got %q", zero.Label())
	}
}

// TestToolChoice_Label asserts the telemetry label format. Tests rely on
// these strings; downstream dashboards may too.
func TestToolChoice_Label(t *testing.T) {
	cases := []struct {
		c    ToolChoice
		want string
	}{
		{ToolChoiceAuto(), "auto"},
		{ToolChoiceRequired(), "required"},
		{ToolChoiceNone(), "none"},
		{ToolChoiceSpecific("publish"), "specific:publish"},
	}
	for _, tc := range cases {
		if got := tc.c.Label(); got != tc.want {
			t.Errorf("Label(%v) = %q, want %q", tc.c, got, tc.want)
		}
	}
}
