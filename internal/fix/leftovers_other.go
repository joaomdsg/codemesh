//go:build !linux

package fix

// endMarked has no process table to search here; what left its process
// group outlives the run.
func endMarked(string) {}
