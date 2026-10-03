package protocol

import "os"

func ensureDir(path string) error { return os.MkdirAll(path, 0755) }
