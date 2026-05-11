package tool

import "sync"

// ToolRegistry is a thread-safe registry for tools. Used by Toolkits and Skills
// to register their tools via the same functional API as NewTool.
type ToolRegistry struct {
	mu    sync.RWMutex
	tools []*Tool
}

// NewToolRegistry creates an empty tool registry.
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		tools: make([]*Tool, 0),
	}
}

// Register adds a tool to the registry. The schema and fn parameters use the
// same signature as NewTool but fn is typed as any for registration-time
// wrapping. The schema struct type and fn input type must match.
func (r *ToolRegistry) Register(schema any, fn any, cfg ToolConfig) {
	// fn must be func(context.Context, T) (string, error) where T matches schema type.
	// We use reflection-free wrapping: the Tool struct stores a generic closure.
	t := wrapTool(schema, fn, cfg)

	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools = append(r.tools, t)
}

// Add adds a pre-built *Tool to the registry.
func (r *ToolRegistry) Add(t *Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools = append(r.tools, t)
}

// Tools returns a copy of all registered tools.
func (r *ToolRegistry) Tools() []*Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cp := make([]*Tool, len(r.tools))
	copy(cp, r.tools)
	return cp
}

// Len returns the number of registered tools.
func (r *ToolRegistry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tools)
}

// wrapTool creates a *Tool from untyped schema and fn parameters using
// runtime type information. It extracts the input type from fn's signature
// and generates the schema from the provided example struct.
//
// This is the internal bridge between the untyped Register method and
// the type-safe NewTool constructor.
func wrapTool(schema any, fn any, cfg ToolConfig) *Tool {
	// At registration time, we call NewTool with the schema as both
	// the schema source and the type parameter. The fn closure is
	// already typed correctly at the call site.
	//
	// We use a helper that captures the fn via interface{} assertion
	// to avoid requiring the caller to specify type parameters.
	return newToolFromAny(schema, fn, cfg)
}
