// Package google: JSON Schema → genai.Schema conversion.
//
// Gemini's API accepts a closed subset of OpenAPI 3.0 Schema: no $ref, no
// $defs, no allOf/oneOf. Our tool inputs are described as JSON Schema by
// invopop/jsonschema, which uses $ref/$defs to represent reused or
// recursive Go types. This file bridges the two formats in two phases:
//
//  1. flattenRefs walks the schema map and replaces every $ref with a deep
//     copy of its $defs target. Cyclic references (recursive Go types such
//     as `type Section struct { Children []Section }`) are detected by
//     tracking the ref names already being resolved on the current path
//     and collapsed to a permissive object schema on re-entry — the model
//     still sees the surrounding shape but recursion terminates.
//
//  2. mapToGenaiSchema is a pure map → *genai.Schema translator. It assumes
//     the input has already been flattened and covers every field
//     genai.Schema exposes (items, anyOf, format, nullable, all min/max
//     bounds, default, example, propertyOrdering, etc.).
//
// convertSchema is the only exported-to-package entry point.
package google

import (
	"strings"

	genai "google.golang.org/genai"
)

// convertSchema turns an invopop-style JSON Schema (with $ref/$defs and
// possibly cyclic) into a *genai.Schema accepted by the Gemini API.
//
// A nil or empty input yields a nil schema, mirroring the caller contract
// in google.go (an absent Parameters means "no arguments").
func convertSchema(m map[string]any) *genai.Schema {
	if m == nil {
		return nil
	}
	defs, _ := m["$defs"].(map[string]any)
	flat := flattenRefs(m, defs, nil)
	return mapToGenaiSchema(flat)
}

// flattenRefs returns a deep copy of node with every $ref substituted by
// (a deep copy of) its $defs target. visitedPath holds the names of refs
// currently being resolved on this branch of the walk; if a ref re-enters
// a name already on the path the result is a permissive object schema so
// recursive types terminate instead of looping forever.
//
// visitedPath uses copy-on-extend semantics so sibling branches see their
// own paths independently — a $ref that appears in two unrelated fields
// is fully inlined in both, not falsely flagged as a cycle.
func flattenRefs(node map[string]any, defs map[string]any, visitedPath map[string]bool) map[string]any {
	if node == nil {
		return nil
	}

	if ref, ok := node["$ref"].(string); ok && ref != "" {
		name, ok := refName(ref)
		if !ok {
			return permissiveObject("unresolved $ref: " + ref)
		}
		if visitedPath[name] {
			return permissiveObject("recursive reference to " + name)
		}
		target, ok := defs[name].(map[string]any)
		if !ok {
			return permissiveObject("missing $defs entry: " + name)
		}
		return flattenRefs(target, defs, extendPath(visitedPath, name))
	}

	out := make(map[string]any, len(node))
	for k, v := range node {
		// $defs / $id / $schema are JSON Schema bookkeeping that has no
		// place in the flattened Gemini-shaped output.
		if k == "$defs" || k == "$id" || k == "$schema" {
			continue
		}
		out[k] = flattenValue(v, defs, visitedPath)
	}
	return out
}

// flattenValue dispatches per JSON type. Maps recurse through flattenRefs
// (which is where $ref resolution happens); slices walk element-wise;
// scalars pass through unchanged.
func flattenValue(v any, defs map[string]any, visitedPath map[string]bool) any {
	switch t := v.(type) {
	case map[string]any:
		return flattenRefs(t, defs, visitedPath)
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = flattenValue(item, defs, visitedPath)
		}
		return out
	default:
		return v
	}
}

// refName extracts the type name from a JSON pointer of the form
// "#/$defs/TypeName". Returns ("", false) for any other shape — invopop
// does not emit nested pointers or escaped characters, so we refuse to
// guess on those rather than producing a wrong inline.
func refName(ref string) (string, bool) {
	const prefix = "#/$defs/"
	if !strings.HasPrefix(ref, prefix) {
		return "", false
	}
	name := ref[len(prefix):]
	if name == "" || strings.ContainsAny(name, "/~") {
		return "", false
	}
	return name, true
}

// extendPath returns a new visitedPath with name added. The input map is
// left untouched so concurrent sibling branches keep independent state.
func extendPath(visited map[string]bool, name string) map[string]bool {
	next := make(map[string]bool, len(visited)+1)
	for k := range visited {
		next[k] = true
	}
	next[name] = true
	return next
}

// permissiveObject is the fallback shape used when a ref cannot be
// inlined: a cycle, a missing $defs entry, or a malformed pointer. A bare
// object with a description preserves a hint for the model without
// imposing structure we can't actually validate downstream.
func permissiveObject(desc string) map[string]any {
	return map[string]any{
		"type":        "object",
		"description": desc,
	}
}

// mapToGenaiSchema is a pure JSON Schema map → *genai.Schema translator.
// It assumes refs are already inlined (see flattenRefs) and covers every
// field the genai.Schema struct exposes; fields absent from the input map
// are simply omitted from the output.
func mapToGenaiSchema(m map[string]any) *genai.Schema {
	if m == nil {
		return nil
	}
	s := &genai.Schema{}

	if v, ok := m["type"].(string); ok {
		s.Type = normalizeType(v)
	}
	if v, ok := m["description"].(string); ok {
		s.Description = v
	}
	if v, ok := m["format"].(string); ok {
		s.Format = v
	}
	if v, ok := m["pattern"].(string); ok {
		s.Pattern = v
	}
	if v, ok := m["title"].(string); ok {
		s.Title = v
	}
	if v, ok := m["default"]; ok {
		s.Default = v
	}
	if v, ok := m["example"]; ok {
		s.Example = v
	}
	if v, ok := m["nullable"].(bool); ok {
		s.Nullable = &v
	}

	if v, ok := stringSlice(m["enum"]); ok {
		s.Enum = v
	}
	if v, ok := stringSlice(m["required"]); ok {
		s.Required = v
	}
	if v, ok := stringSlice(m["propertyOrdering"]); ok {
		s.PropertyOrdering = v
	}

	if v, ok := numberPtr(m["minimum"]); ok {
		s.Minimum = v
	}
	if v, ok := numberPtr(m["maximum"]); ok {
		s.Maximum = v
	}
	if v, ok := intPtr(m["minLength"]); ok {
		s.MinLength = v
	}
	if v, ok := intPtr(m["maxLength"]); ok {
		s.MaxLength = v
	}
	if v, ok := intPtr(m["minItems"]); ok {
		s.MinItems = v
	}
	if v, ok := intPtr(m["maxItems"]); ok {
		s.MaxItems = v
	}
	if v, ok := intPtr(m["minProperties"]); ok {
		s.MinProperties = v
	}
	if v, ok := intPtr(m["maxProperties"]); ok {
		s.MaxProperties = v
	}

	if items, ok := m["items"].(map[string]any); ok {
		s.Items = mapToGenaiSchema(items)
	}

	if props, ok := m["properties"].(map[string]any); ok {
		s.Properties = make(map[string]*genai.Schema, len(props))
		for name, propVal := range props {
			if propMap, ok := propVal.(map[string]any); ok {
				s.Properties[name] = mapToGenaiSchema(propMap)
			}
		}
	}

	if alts, ok := m["anyOf"].([]any); ok {
		for _, alt := range alts {
			if altMap, ok := alt.(map[string]any); ok {
				s.AnyOf = append(s.AnyOf, mapToGenaiSchema(altMap))
			}
		}
	}

	return s
}

// stringSlice accepts either []string or []any-of-strings (which JSON
// unmarshal produces) and returns a normalized []string. (nil, false) on
// missing value or any non-string element.
func stringSlice(v any) ([]string, bool) {
	switch t := v.(type) {
	case nil:
		return nil, false
	case []string:
		return t, true
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			s, ok := item.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	}
	return nil, false
}

// numberPtr coerces a number-like JSON value to *float64. JSON unmarshal
// yields float64 for all numbers but invopop tags can also surface int;
// accept both for resilience.
func numberPtr(v any) (*float64, bool) {
	switch t := v.(type) {
	case nil:
		return nil, false
	case float64:
		return &t, true
	case float32:
		f := float64(t)
		return &f, true
	case int:
		f := float64(t)
		return &f, true
	case int64:
		f := float64(t)
		return &f, true
	}
	return nil, false
}

// normalizeType maps JSON Schema's lowercase primitive names to the
// uppercase constants Gemini's genai SDK exposes. Unknown values pass
// through verbatim so non-standard extensions reach the API and surface
// as proper Gemini-side errors rather than being silently dropped.
func normalizeType(t string) genai.Type {
	switch t {
	case "string":
		return genai.TypeString
	case "number":
		return genai.TypeNumber
	case "integer":
		return genai.TypeInteger
	case "boolean":
		return genai.TypeBoolean
	case "array":
		return genai.TypeArray
	case "object":
		return genai.TypeObject
	case "null":
		return genai.TypeNULL
	}
	return genai.Type(t)
}

// intPtr coerces a number-like JSON value to *int64 for length/count
// limits exposed by genai.Schema.
func intPtr(v any) (*int64, bool) {
	switch t := v.(type) {
	case nil:
		return nil, false
	case int64:
		return &t, true
	case int:
		i := int64(t)
		return &i, true
	case float64:
		i := int64(t)
		return &i, true
	case float32:
		i := int64(t)
		return &i, true
	}
	return nil, false
}
