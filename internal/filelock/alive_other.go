//go:build !unix && !windows

package filelock

// Liveness cannot be checked here; never reclaim a lock with a valid owner.
func alive(int) bool { return true }
