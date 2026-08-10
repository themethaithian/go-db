package app

import "testing"

// TestIsOutdated is a pure table test over the version comparison —
// CheckForUpdate's only piece of logic that doesn't touch the network, per
// CLAUDE.md's TDD-for-domain-logic norm applied here at the adapter level:
// the comparison itself is worth pinning down even though the HTTP call
// around it isn't.
func TestIsOutdated(t *testing.T) {
	tests := []struct {
		name    string
		current string
		latest  string
		want    bool
	}{
		{"dev is never outdated", "dev", "v9.9.9", false},
		{"equal versions", "v0.1.0", "v0.1.0", false},
		{"equal versions without v prefix", "0.1.0", "0.1.0", false},
		{"newer patch available", "v0.1.0", "v0.1.1", true},
		{"newer minor available", "v0.1.0", "v0.2.0", true},
		{"issue example: 0.0.1 vs 0.1.0", "v0.0.1", "v0.1.0", true},
		{"current already newer than latest", "v1.0.0", "v0.9.0", false},
		{"numeric not lexicographic compare", "v1.2.3", "v1.2.10", true},
		{"numeric not lexicographic compare, reversed", "v1.2.10", "v1.2.3", false},
		{"different field counts, latest longer", "v1.0", "v1.0.1", true},
		{"different field counts, equal value", "v1.0", "v1.0.0", false},
		{"malformed current", "not-a-version", "v1.0.0", false},
		{"malformed latest", "v1.0.0", "not-a-version", false},
		{"empty latest (failed/offline check)", "v1.0.0", "", false},
		{"empty current", "", "v1.0.0", false},
		{"prerelease suffix on latest is ignored", "v1.0.0", "v1.1.0-rc1", true},
		{"build metadata suffix is ignored", "v1.0.0", "v1.0.0+build5", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isOutdated(tt.current, tt.latest)
			if got != tt.want {
				t.Errorf("isOutdated(%q, %q) = %v, want %v", tt.current, tt.latest, got, tt.want)
			}
		})
	}
}
