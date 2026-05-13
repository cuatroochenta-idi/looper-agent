package anthropic

import (
	"github.com/anthropics/anthropic-sdk-go"

	"github.com/cuatroochenta-idi/looper-agent/provider"
)

// buildToolChoiceParams maps a universal ToolChoice into Anthropic's
// ToolChoiceUnionParam. Returns a pointer so the caller can decide whether
// to set the field on the request — Auto with no tools is still the wire
// default and need not be serialized.
//
// Mapping:
//   - Auto     → {"type":"auto"}
//   - Required → {"type":"any"}     (Anthropic spells "force a tool" as "any")
//   - None     → {"type":"none"}
//   - Specific → {"type":"tool","name":"..."}
func buildToolChoiceParams(c provider.ToolChoice) *anthropic.ToolChoiceUnionParam {
	switch c.Kind {
	case provider.ToolChoiceKindAuto:
		return &anthropic.ToolChoiceUnionParam{
			OfAuto: &anthropic.ToolChoiceAutoParam{},
		}
	case provider.ToolChoiceKindRequired:
		return &anthropic.ToolChoiceUnionParam{
			OfAny: &anthropic.ToolChoiceAnyParam{},
		}
	case provider.ToolChoiceKindNone:
		return &anthropic.ToolChoiceUnionParam{
			OfNone: &anthropic.ToolChoiceNoneParam{},
		}
	case provider.ToolChoiceKindSpecific:
		u := anthropic.ToolChoiceParamOfTool(c.Name)
		return &u
	}
	return nil
}
