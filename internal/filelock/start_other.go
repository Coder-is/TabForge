//go:build !linux && !windows

package filelock

import "time"

// Creation times are not checked here; a live PID always keeps its lock.
func processStart(int) (time.Time, bool) { return time.Time{}, false }
