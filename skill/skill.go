// Package skill defines the Skill interface for the Looper Agent framework.
//
// A skill groups related tools with an injectable prompt fragment.
// Unlike a Toolkit, a Skill can modify the system prompt and provides
// a complete thematic context (tools + instructions).
package skill

import "github.com/cuatroochenta-idi/looper-agent/tool"

// Skill groups tools with discovery metadata and a prompt fragment. Every
// skill — eager or lazy — exposes the same API; laziness is only a behavioral
// marker (see LazySkill), never a different content contract.
//
// Skills can share internal state between their tools, their metadata, and
// their prompt fragment.
type Skill interface {
	// Name returns the stable, unique skill identifier (used by load_skill).
	Name() string

	// Title returns a short, human-readable title.
	Title() string

	// Summary returns a one- or two-line description of when and why to use
	// this skill. It is what appears in the skills index so the model can
	// decide whether the skill is relevant.
	Summary() string

	// RegisterTools registers this skill's tools in the provided registry.
	RegisterTools(reg *tool.ToolRegistry)

	// PromptFragment returns the FULL, detailed prompt for this skill (lots of
	// text). For eager skills it is concatenated into the system prompt; for
	// lazy skills it is delivered on demand via the load_skill tool result.
	// It can reference the skill's internal state (e.g., language, config).
	PromptFragment() string
}

// LazySkill is a Skill that loads on demand via the auto-injected load_skill
// tool. Its API is IDENTICAL to Skill; laziness is just a behavioral marker.
//
// Embed skill.Lazy into a skill struct to opt in. Until the model loads it,
// only Title()+Summary() appear in the skills index; the skill's tools and
// its full PromptFragment activate only after load_skill is called.
type LazySkill interface {
	Skill

	// isLazySkill is an unexported marker satisfied by embedding skill.Lazy.
	isLazySkill()
}

// Lazy is embedded into a skill struct to mark it load-on-demand, turning a
// Skill into a LazySkill without changing any other part of its API.
//
//	type TranslatorSkill struct {
//	    skill.Lazy
//	    TargetLang string
//	}
type Lazy struct{}

// isLazySkill marks the embedding type as a LazySkill.
func (Lazy) isLazySkill() {}
