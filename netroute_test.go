package main

import "testing"

func TestMatchPattern(t *testing.T) {
	cases := []struct {
		pattern, url string
		want         bool
	}{
		{"", "https://x", false},
		{"api.example", "https://api.example.com/v1", true},
		{"nomatch", "https://other", false},
		{"*.example.com/*", "https://sub.example.com/x", true},
		{"*.example.com/*", "https://example.org/y", false},
		{"a?c", "abc", true},
		{"a?c", "azc", true},
		{"a?c", "ac", false},
		{"**", "anything", true},
	}
	for _, c := range cases {
		if got := matchPattern(c.pattern, c.url); got != c.want {
			t.Errorf("matchPattern(%q, %q) = %v, want %v", c.pattern, c.url, got, c.want)
		}
	}
}

func TestGlobMatchEdgeCases(t *testing.T) {
	if !globMatch("*x*", "axe") {
		t.Error("globMatch(*x*, axe) = false")
	}
	if globMatch("x*", "yx") {
		t.Error("globMatch(x*, yx) = true, want false (anchored)")
	}
	if !globMatch("a*", "a") {
		t.Error("globMatch(a*, a) = false")
	}
}

func TestParseRawAndUnwrap(t *testing.T) {
	if parseRaw(nil) != nil {
		t.Error("parseRaw(nil) != nil")
	}
	// WebDriver BiDi remote value: {type:..., value:42} unwraps to 42.
	m := map[string]any{"type": "number", "value": 42}
	if v, ok := unwrapRemoteVal(m).(int); !ok || v != 42 {
		t.Errorf("unwrapRemoteVal(remote 42) = %v (%T)", unwrapRemoteVal(m), unwrapRemoteVal(m))
	}
	// undefined/null remote values collapse to nil.
	if unwrapRemoteVal(map[string]any{"type": "undefined"}) != nil {
		t.Error("unwrapRemoteVal(undefined) != nil")
	}
}
