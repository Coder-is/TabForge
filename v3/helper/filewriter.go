package helper

import (
	"os"
	"path/filepath"
)

func WriteFile(filename string, data []byte) error {

	if err := os.MkdirAll(filepath.Dir(filename), 0755); err != nil {
		return err
	}

	return os.WriteFile(filename, data, 0666)
}
