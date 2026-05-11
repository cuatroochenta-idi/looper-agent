// Package toolkit defines the Toolkit interface for the Looper Agent framework.
//
// A toolkit groups related tools that share internal state (API keys, caches,
// rate limiters). Unlike a Skill, a Toolkit does NOT modify the system prompt.
package toolkit

import "github.com/cuatroochenta-idi/looper-agent/tool"

// Toolkit groups related tools with shared internal state.
// The RegisterTools method receives a ToolRegistry to register
// tools using the same functional API as NewTool.
type Toolkit interface {
	// RegisterTools registers this toolkit's tools in the provided registry.
	RegisterTools(reg *tool.ToolRegistry)
}
