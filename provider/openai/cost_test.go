package openai

import "testing"

func TestExtractCostField(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want float64
	}{
		{
			name: "openrouter usage.cost",
			raw:  `{"prompt_tokens":100,"completion_tokens":50,"cost":0.0123}`,
			want: 0.0123,
		},
		{
			name: "cost as integer",
			raw:  `{"cost":2}`,
			want: 2,
		},
		{
			name: "no cost field",
			raw:  `{"prompt_tokens":100,"completion_tokens":50}`,
			want: 0,
		},
		{
			name: "empty",
			raw:  "",
			want: 0,
		},
		{
			name: "malformed json",
			raw:  `{not json`,
			want: 0,
		},
		{
			name: "cost wrong type ignored",
			raw:  `{"cost":"oops"}`,
			want: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractCostField(tt.raw); got != tt.want {
				t.Errorf("extractCostField(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}
