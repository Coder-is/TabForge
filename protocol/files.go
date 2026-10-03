package protocol

import (
	"os"
	"path/filepath"
)

func ensureDir(path string) error { return os.MkdirAll(path, 0755) }

// Replace each file atomically on the same filesystem. A bundle is not an atomic
// directory transaction; publish a completed directory as one deployment unit.
func atomicWrite(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".tabforge-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0644); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
