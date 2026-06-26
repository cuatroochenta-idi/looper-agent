package loop

import "testing"

// TestNormalizeStructuredOutput covers the double-encoding repair: a JSON
// string wrapping an object/array is unwrapped one level; a real object,
// array, or plain-string answer is left as-is.
func TestNormalizeStructuredOutput(t *testing.T) {
	cases := map[string]struct{ in, want string }{
		"object as-is":          {`{"message":"hi"}`, `{"message":"hi"}`},
		"array as-is":           {`[1,2]`, `[1,2]`},
		"double-encoded object": {`"{\"message\": \"hi\"}"`, `{"message": "hi"}`},
		"double-encoded array":  {`"[1,2]"`, `[1,2]`},
		"genuine string answer": {`"just a string"`, `"just a string"`},
		"whitespace + wrapped":  {`  "{\"a\":1}"  `, `{"a":1}`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := normalizeStructuredOutput(c.in); got != c.want {
				t.Errorf("normalizeStructuredOutput(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
