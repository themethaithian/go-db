//go:build !darwin

package app

// memoryUsageBytes has no non-cgo, non-darwin implementation yet (go-db is
// Mac-first — see CLAUDE.md). It always reports failure so MemoryUsageMB
// falls back to runtime.MemStats.Sys, keeping non-darwin builds possible.
func memoryUsageBytes() (bytes uint64, ok bool) {
	return 0, false
}
