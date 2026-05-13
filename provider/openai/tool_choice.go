package openai

import (
	"github.com/openai/openai-go"
	"github.com/openai/openai-go/packages/param"

	"github.com/cuatroochenta-idi/looper-agent/provider"
)

// buildToolChoiceParams maps a universal ToolChoice into the OpenAI SDK's
// ChatCompletionToolChoiceOptionUnionParam. Returns a pointer so the
// caller can check for nil (== "no toolchoice configured, use API
// default"). The zero ToolChoice maps to "auto" so existing callers keep
// the same wire shape they had before this field existed.
//
// Mapping (per OpenAI's chat-completions API):
//   - Auto     → bare string "auto"
//   - Required → bare string "required"
//   - None     → bare string "none"
//   - Specific → {"type":"function","function":{"name":"..."}}
func buildToolChoiceParams(c provider.ToolChoice) *openai.ChatCompletionToolChoiceOptionUnionParam {
	switch c.Kind {
	case provider.ToolChoiceKindAuto:
		u := openai.ChatCompletionToolChoiceOptionUnionParam{OfAuto: param.NewOpt("auto")}
		return &u
	case provider.ToolChoiceKindRequired:
		u := openai.ChatCompletionToolChoiceOptionUnionParam{OfAuto: param.NewOpt("required")}
		return &u
	case provider.ToolChoiceKindNone:
		u := openai.ChatCompletionToolChoiceOptionUnionParam{OfAuto: param.NewOpt("none")}
		return &u
	case provider.ToolChoiceKindSpecific:
		u := openai.ChatCompletionToolChoiceOptionParamOfChatCompletionNamedToolChoice(
			openai.ChatCompletionNamedToolChoiceFunctionParam{Name: c.Name},
		)
		return &u
	}
	return nil
}
