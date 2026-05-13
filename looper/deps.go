package looper

import "context"

// WithRunDeps stores a typed dependency value on ctx so tools, hooks,
// and validators can retrieve it later via Deps[T]. The dependency is
// scoped to the goroutine reading from this ctx — concurrent agent runs
// each carry their own deps without sharing global state.
//
// Typical wiring:
//
//	type Deps struct { DB *sql.DB; UserID string }
//	ctx := looper.WithRunDeps(ctx, Deps{DB: db, UserID: u})
//	res, _ := agent.Run(ctx, "do something")
//
// Inside a tool:
//
//	deps, ok := looper.Deps[Deps](ctx)
//
// Pydantic-AI users will recognize this as the moral equivalent of
// `RunContext[Deps]` — the Go translation drops the synthetic context
// wrapper and uses the language's standard context.Context.
func WithRunDeps[T any](ctx context.Context, deps T) context.Context {
	return context.WithValue(ctx, depsKey[T]{}, deps)
}

// Deps retrieves a typed dependency previously stored with WithRunDeps.
// Returns the zero value plus ok=false when nothing of type T was set —
// no panic, no type assertion at the call site.
func Deps[T any](ctx context.Context) (T, bool) {
	if ctx == nil {
		var zero T
		return zero, false
	}
	v, ok := ctx.Value(depsKey[T]{}).(T)
	return v, ok
}

// depsKey is a type-parameterised, unexported key so two different deps
// types stored on the same ctx don't collide. The generic instantiation
// is what gives each T its own key.
type depsKey[T any] struct{}
