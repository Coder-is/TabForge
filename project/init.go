package project

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Init copies a project template without overwriting any existing file.
// A portable tool can additionally be placed in Tools/TabForge by its caller.
func Init(dir string, template fs.FS) error {
	root, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	var names []string
	if err := fs.WalkDir(template, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		path, err := relativePath(root, name)
		if err != nil {
			return err
		}
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("initialization would overwrite %s", path)
		} else if !os.IsNotExist(err) {
			return err
		}
		names = append(names, name)
		return nil
	}); err != nil {
		return err
	}
	for _, name := range names {
		data, err := fs.ReadFile(template, name)
		if err != nil {
			return err
		}
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		mode := os.FileMode(0644)
		if filepath.Ext(name) == ".command" {
			mode = 0755
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		_, writeErr := f.Write(data)
		closeErr := f.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
