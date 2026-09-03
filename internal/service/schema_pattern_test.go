package service

// This is the one internal test in this package — package service rather than
// service_test — because what it pins is a rule the facade hides: how the text
// a human types in the Explorer's filter box becomes the glob SCAN's MATCH
// takes. The rule has no exported form, and every FindKeys test beside it in
// schema_engine_test.go reads it through a command line, where a wrong pattern
// and a wrong quoting look the same. This says which is which.

import "testing"

func TestRedisMatchPattern(t *testing.T) {
	for _, test := range []struct {
		name string
		text string
		want string
	}{
		{"plain text is a substring search", "user", "*user*"},
		{"a colon is not a metacharacter", "user:1", "*user:1*"},
		{"a space is part of the substring", "my key", "*my key*"},
		{"a star means the human wrote the pattern", "user:*", "user:*"},
		{"so does a question mark", "user:?", "user:?"},
		{"so does a character class", "user:[0-9]", "user:[0-9]"},
		{"a pattern is not wrapped again", "*user*", "*user*"},
		{"a backslash in a substring is escaped for the glob", `a\b`, `*a\\b*`},
		{"a backslash in a pattern is the human's own escape", `a\*b*`, `a\*b*`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := redisMatchPattern(test.text); got != test.want {
				t.Errorf("redisMatchPattern(%q) = %q, want %q", test.text, got, test.want)
			}
		})
	}
}
