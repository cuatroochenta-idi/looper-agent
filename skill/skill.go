// Package skill defines the Skill interface for the Looper Agent framework.
//
// A skill groups related tools with an injectable prompt fragment.
// Unlike a Toolkit, a Skill can modify the system prompt and provides
// a complete thematic context (tools + instructions).
package skill

import "github.com/cuatroochenta-idi/looper-agent/tool"

// Skill groups tools with a prompt fragment that is concatenated to the
// agent's system prompt. Skills can share internal state between their
// tools and their prompt fragment.
type Skill interface {
	// Name returns the unique skill identifier.
	Name() string

	// RegisterTools registers this skill's tools in the provided registry.
	RegisterTools(reg *tool.ToolRegistry)

	// PromptFragment returns a string appended to the system prompt.
	// It can reference the skill's internal state (e.g., language, config).
	PromptFragment() string
}
