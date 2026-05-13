package tool

import (
	"context"
	"encoding/json"
	"errors"
)

// RejectionError is the sentinel error returned by RejectWithHint. The
// agent loop treats it as a tool_result with IsError=true so the LLM
// sees the rejection text as corrective feedback — distinct from a real
// execution failure that might warrant a retry.
type RejectionError struct {
	Hint string
}

// Error implements the error interface.
func (e *RejectionError) Error() string { return e.Hint }

// RejectWithHint builds a RejectionError carrying a human-readable hint.
// Return it from a PreExecute hook to abort the tool call cleanly and
// route the hint to the model.
//
// Example:
//
//	WithPreExecute(func(ctx context.Context, in CompletePRDInput) error {
//	    if !tracker.Has("publish_pages.ok") {
//	        return tool.RejectWithHint("publish_pages must succeed first")
//	    }
//	    return nil
//	})
func RejectWithHint(hint string) error {
	return &RejectionError{Hint: hint}
}

// IsRejection reports whether err originated from RejectWithHint. The
// agent loop uses this to convert rejections into tool_result feedback
// instead of letting them bubble as run failures.
func IsRejection(err error) bool {
	var r *RejectionError
	return errors.As(err, &r)
}

// toolOptions is the internal builder populated by ToolOption functions.
// Stored on Tool after construction.
type toolOptions struct {
	preExecute func(ctx context.Context, args json.RawMessage) error
}

// ToolOption mutates the tool's optional configuration. Used by
// NewTool / MustNewTool variadic argument. Construct one via the
// With* helpers in this package.
type ToolOption func(*toolOptions)

// WithPreExecute registers a business-validation function that runs
// before the tool body. It receives the same typed input the tool body
// receives. Return RejectWithHint(...) to abort with corrective feedback;
// return any other error to fail the call (no retry).
//
// The generic parameter must match the schema type passed to NewTool;
// the framework json-unmarshals the LLM's arguments into a fresh value
// of I before invoking fn.
func WithPreExecute[I any](fn func(ctx context.Context, input I) error) ToolOption {
	return func(opts *toolOptions) {
		opts.preExecute = func(ctx context.Context, args json.RawMessage) error {
			var input I
			if err := json.Unmarshal(args, &input); err != nil {
				return err
			}
			return fn(ctx, input)
		}
	}
}
