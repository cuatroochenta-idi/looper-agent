package google

import (
	"google.golang.org/genai"

	"github.com/cuatroochenta-idi/looper-agent/provider"
)

// buildToolConfig maps a universal ToolChoice into Gemini's
// FunctionCallingConfig. Returns nil when the field would be redundant
// (Auto without a specific name).
//
// Mapping:
//   - Auto     → Mode = AUTO
//   - Required → Mode = ANY
//   - None     → Mode = NONE
//   - Specific → Mode = ANY + AllowedFunctionNames = [name]
//
// Gemini doesn't have a dedicated "must call exactly this tool" mode; the
// closest match is ANY with a single-element AllowedFunctionNames, which
// constrains the model to that one function. Empirically that's what the
// google-genai SDK examples recommend.
func buildToolConfig(c provider.ToolChoice) *genai.ToolConfig {
	switch c.Kind {
	case provider.ToolChoiceKindAuto:
		return &genai.ToolConfig{
			FunctionCallingConfig: &genai.FunctionCallingConfig{
				Mode: genai.FunctionCallingConfigModeAuto,
			},
		}
	case provider.ToolChoiceKindRequired:
		return &genai.ToolConfig{
			FunctionCallingConfig: &genai.FunctionCallingConfig{
				Mode: genai.FunctionCallingConfigModeAny,
			},
		}
	case provider.ToolChoiceKindNone:
		return &genai.ToolConfig{
			FunctionCallingConfig: &genai.FunctionCallingConfig{
				Mode: genai.FunctionCallingConfigModeNone,
			},
		}
	case provider.ToolChoiceKindSpecific:
		return &genai.ToolConfig{
			FunctionCallingConfig: &genai.FunctionCallingConfig{
				Mode:                 genai.FunctionCallingConfigModeAny,
				AllowedFunctionNames: []string{c.Name},
			},
		}
	}
	return nil
}
