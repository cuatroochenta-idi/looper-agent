package provider

// ToolChoiceKind discriminates the four ways a caller can constrain tool
// selection on a turn. Each provider translator switches on Kind to emit
// the right native shape.
type ToolChoiceKind int

const (
	// ToolChoiceKindAuto lets the model decide. Default when Kind is unset.
	ToolChoiceKindAuto ToolChoiceKind = iota

	// ToolChoiceKindRequired forces the model to call SOME tool.
	ToolChoiceKindRequired

	// ToolChoiceKindNone forbids tool calls — model must answer with text.
	ToolChoiceKindNone

	// ToolChoiceKindSpecific forces the model to call the named tool.
	ToolChoiceKindSpecific
)

// ToolChoice is a value-typed union over the ways callers can constrain
// tool selection. The zero value means "auto" — leaving the field unset
// in LLMRequest is equivalent to ToolChoiceAuto().
//
// Per-provider mapping:
//
//   - OpenAI: Auto → "auto", Required → "required", None → "none",
//     Specific(name) → {"type":"function","function":{"name":name}}.
//   - Anthropic: Auto → {"type":"auto"}, Required → {"type":"any"},
//     None → {"type":"none"}, Specific(name) → {"type":"tool","name":name}.
//   - Gemini: Auto → FunctionCallingConfig{Mode:"AUTO"},
//     Required → Mode:"ANY", None → Mode:"NONE",
//     Specific(name) → Mode:"ANY" + AllowedFunctionNames:[name].
type ToolChoice struct {
	Kind ToolChoiceKind
	Name string // populated only when Kind == ToolChoiceKindSpecific
}

// ToolChoiceAuto lets the model decide whether to call a tool — the default.
func ToolChoiceAuto() ToolChoice { return ToolChoice{Kind: ToolChoiceKindAuto} }

// ToolChoiceRequired forces the model to call some tool. Useful for the
// first turn of a strict planning agent (a TurnValidator can flip back to
// Auto once a tool was actually called).
func ToolChoiceRequired() ToolChoice { return ToolChoice{Kind: ToolChoiceKindRequired} }

// ToolChoiceNone forbids tool calls — the model must answer with text.
func ToolChoiceNone() ToolChoice { return ToolChoice{Kind: ToolChoiceKindNone} }

// ToolChoiceSpecific forces the model to call the named tool on this turn.
// Use for state-machine transitions ("now run publish_pages") or
// validator-driven re-prompts ("you forgot to cite — call cite_source").
func ToolChoiceSpecific(name string) ToolChoice {
	return ToolChoice{Kind: ToolChoiceKindSpecific, Name: name}
}

// Label returns a stable string ("auto" / "required" / "none" /
// "specific:<name>") for tracing and telemetry. The zero value renders
// as "auto".
func (c ToolChoice) Label() string {
	switch c.Kind {
	case ToolChoiceKindAuto:
		return "auto"
	case ToolChoiceKindRequired:
		return "required"
	case ToolChoiceKindNone:
		return "none"
	case ToolChoiceKindSpecific:
		return "specific:" + c.Name
	default:
		return "unknown"
	}
}
