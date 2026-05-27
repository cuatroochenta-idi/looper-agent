package provider

import "testing"

func TestAPIKeySuffix(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"too short", "abc", ""},
		{"exactly four", "abcd", "****abcd"},
		{"typical key", "sk-proj-AbCdEfGhIjKlMnOpQrSta2Fn", "****a2Fn"},
		{"unicode tail", "key🙂abcd", "****abcd"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := APIKeySuffix(c.in)
			if got != c.want {
				t.Fatalf("APIKeySuffix(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
