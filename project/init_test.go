package project

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

// A tool built from a Windows autocrlf checkout embeds CRLF launchers; the
// macOS launcher must still start with a plain "#!/bin/bash" line.
func TestInitWritesUnixLaunchers(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "project")
	template := fstest.MapFS{"Tools/Export.command": {Data: []byte("#!/bin/bash\r\necho ok\r\n")}}
	if err := Init(dir, template); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "Tools", "Export.command"))
	if err != nil || string(data) != "#!/bin/bash\necho ok\n" {
		t.Fatalf("launcher: %q %v", data, err)
	}
}
