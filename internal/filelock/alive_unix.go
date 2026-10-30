//go:build unix

package filelock

import (
	"errors"
	"syscall"
)

func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	// EPERM means the process exists but belongs to another user.
	return err == nil || !errors.Is(err, syscall.ESRCH)
}
