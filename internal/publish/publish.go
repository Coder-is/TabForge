// Package publish swaps a fully staged generated directory into place while
// keeping the last good output recoverable. Callers must hold the directory's
// lock (see internal/filelock) for Recover and Dir.
package publish

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// BackupSuffix names the sibling that holds the previous output during a swap.
const BackupSuffix = ".tabforge-backup"

// Recover repairs a swap that a crash or failed cleanup left behind. marker is
// a file that every complete output contains; it proves that out is not an
// unrelated directory before the superseded backup is deleted.
//
//   - out and backup exist: the new output was installed and only the backup
//     cleanup failed, so the backup is the superseded copy and is removed.
//   - only backup exists: the run stopped between moving the old output aside
//     and installing the new one, so the backup is restored.
func Recover(out, marker string) error {
	backup := out + BackupSuffix
	if _, err := os.Lstat(backup); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if _, err := os.Lstat(out); err == nil {
		if info, err := os.Lstat(filepath.Join(out, marker)); err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("previous backup exists at %s and %s is not a complete output; recover it manually", backup, out)
		}
		if err := os.RemoveAll(backup); err != nil {
			return fmt.Errorf("remove superseded backup %s: %w", backup, err)
		}
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Rename(backup, out); err != nil {
		return fmt.Errorf("restore previous output from %s: %w", backup, err)
	}
	return nil
}

// Dir replaces out with stage. If installing fails, the previous output is put
// back. A non-nil warning means stage was installed but the superseded backup
// could not be deleted; the next Recover removes it.
func Dir(stage, out, marker string) (warning, err error) {
	if err := Recover(out, marker); err != nil {
		return nil, err
	}
	backup := out + BackupSuffix
	exists := false
	if _, err := os.Lstat(out); err == nil {
		exists = true
		if err := os.Rename(out, backup); err != nil {
			return nil, fmt.Errorf("back up previous output: %w", err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if err := os.Rename(stage, out); err != nil {
		if exists {
			if rollback := os.Rename(backup, out); rollback != nil {
				return nil, fmt.Errorf("publish failed: %v; rollback failed: %v; previous output is at %s and is restored by the next run", err, rollback, backup)
			}
		}
		return nil, err
	}
	if exists {
		if err := os.RemoveAll(backup); err != nil {
			return fmt.Errorf("output published, but superseded backup %s could not be removed (the next run retries): %w", backup, err), nil
		}
	}
	return nil, nil
}
