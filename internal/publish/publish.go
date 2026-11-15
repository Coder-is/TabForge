// Package publish swaps a fully staged generated directory into place while
// keeping the last good output recoverable. Callers must hold the directory's
// lock (see internal/filelock) for Recover and Dir.
package publish

import (
	"bytes"
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

// Replaceable reports whether out may be replaced by a generated directory:
// it is missing, an empty directory, or a directory holding the regular marker
// file that every complete output contains. Anything else may be handwritten
// and is never moved aside or deleted.
func Replaceable(out, marker string) error {
	info, err := os.Lstat(out)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if info.IsDir() {
		if marked, err := os.Lstat(filepath.Join(out, marker)); err == nil && marked.Mode().IsRegular() {
			return nil
		}
		entries, err := os.ReadDir(out)
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			return nil
		}
	}
	return fmt.Errorf("refusing to replace %s: it exists but is not a generated output (no %s); move or delete it yourself, or choose another output path", out, marker)
}

// Dir replaces out with stage. If installing fails, the previous output is put
// back. A non-nil warning means stage was installed but the superseded backup
// could not be deleted; the next Recover removes it. An existing out that is
// not Replaceable is refused untouched.
func Dir(stage, out, marker string) (warning, err error) {
	if err := Recover(out, marker); err != nil {
		return nil, err
	}
	if err := Replaceable(out, marker); err != nil {
		return nil, err
	}
	if same, err := keepUnchanged(stage, out); err != nil {
		return nil, err
	} else if same {
		// Nothing changed: leave the installed output, and its timestamps, alone.
		return nil, os.RemoveAll(stage)
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

// keepUnchanged gives every staged file whose bytes equal the installed file
// at the same path that file's modification time, so engines that watch
// timestamps (Unity, Godot, Unreal) only reimport what really changed. same
// reports that stage and out hold exactly the same files and contents.
// Comparison is best effort: a file that cannot be read counts as changed.
func keepUnchanged(stage, out string) (same bool, err error) {
	if info, err := os.Lstat(out); err != nil || !info.IsDir() {
		return false, nil
	}
	same = true
	staged := 0
	err = filepath.WalkDir(stage, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		staged++
		if !d.Type().IsRegular() {
			same = false
			return nil
		}
		rel, err := filepath.Rel(stage, path)
		if err != nil {
			return err
		}
		old := filepath.Join(out, rel)
		info, err := os.Lstat(old)
		if err != nil || !info.Mode().IsRegular() || !sameBytes(path, old, info.Size()) {
			same = false
			return nil
		}
		if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
			same = false
		}
		return nil
	})
	if err != nil || !same {
		return false, err
	}
	installed := 0
	walk := filepath.WalkDir(out, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			installed++
		}
		return nil
	})
	return walk == nil && installed == staged, nil
}

func sameBytes(a, b string, size int64) bool {
	if info, err := os.Stat(a); err != nil || info.Size() != size {
		return false
	}
	x, err := os.ReadFile(a)
	if err != nil {
		return false
	}
	y, err := os.ReadFile(b)
	return err == nil && bytes.Equal(x, y)
}
