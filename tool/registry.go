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
//
// Returns an error if schema generation or compilation fails. Use MustRegister
// when the input is known to be valid at compile time (test fixtures, declarative
// tool sets).
func (r *ToolRegistry) Register(schema any, fn any, cfg ToolConfig) error {
	t, err := newToolFromAny(schema, fn, cfg)
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools = append(r.tools, t)
	return nil
}

// MustRegister wraps Register and panics on error. Use in declarative
// registrations where a malformed schema is a programmer error.
func (r *ToolRegistry) MustRegister(schema any, fn any, cfg ToolConfig) {
	if err := r.Register(schema, fn, cfg); err != nil {
		panic(err)
	}
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

