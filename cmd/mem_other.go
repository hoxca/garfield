//go:build !darwin && !linux

package cmd

// physicalMemory is unknown on platforms without a probe, so the worker planner
// falls back to the CPU count alone. Returning 0 disables the memory cap rather
// than guessing a budget that could be wrong by an order of magnitude.
func physicalMemory() int64 {
	return 0
}
