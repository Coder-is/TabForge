package main

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Archives must carry Unix permissions chosen by the packager, not by the host
// filesystem: Windows reports 0666 for every file, which would strip the
// executable bit from macOS tools and .command launchers.
func TestDirectoryArchiveModesAreHostIndependent(t *testing.T) {
	stage := t.TempDir()
	files := map[string]os.FileMode{
		"TabForgeProject/Tools/TabForge/tabforge":       0755,
		"TabForgeProject/Tools/TabForge/tabforge.exe":   0755,
		"TabForgeProject/Tools/TabForge/Export.command": 0755,
		"TabForgeProject/Tools/TabForge/Export.bat":     0644,
		"TabForgeProject/README.md":                     0644,
	}
	for name, mode := range files {
		path := filepath.Join(stage, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		// Write with the opposite permission so host modes cannot leak through.
		if err := os.WriteFile(path, []byte(name), mode^0111); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode^0111); err != nil {
			t.Fatal(err)
		}
	}
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	if err := addDirectory(writer, stage, "TabForgeProject"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(buffer.Bytes()), int64(buffer.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(reader.File) != len(files) {
		t.Fatalf("archived %d files, want %d", len(reader.File), len(files))
	}
	for _, file := range reader.File {
		want, ok := files[file.Name]
		if !ok {
			t.Fatalf("unexpected member %q", file.Name)
		}
		if got := file.Mode(); got != want {
			t.Errorf("%s mode %v, want %v", file.Name, got, want)
		}
	}
}

func TestCopiedLaunchersUseUnixLineEndings(t *testing.T) {
	source, dest := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "Export.command"), []byte("#!/bin/bash\r\necho ok\r\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "Export.bat"), []byte("@echo off\r\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := copyTree(source, dest, func(string) bool { return true }); err != nil {
		t.Fatal(err)
	}
	launcher, _ := os.ReadFile(filepath.Join(dest, "Export.command"))
	if string(launcher) != "#!/bin/bash\necho ok\n" {
		t.Fatalf("macOS launcher keeps CRLF: %q", launcher)
	}
	batch, _ := os.ReadFile(filepath.Join(dest, "Export.bat"))
	if string(batch) != "@echo off\r\n" {
		t.Fatalf("batch file changed: %q", batch)
	}
}

// Every runtime dependency must ship its license; a module without one fails.
func TestThirdPartyLicensesCoverEveryDependency(t *testing.T) {
	licenses, err := thirdPartyLicenses(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for _, module := range []string{"google.golang.org/protobuf@", "github.com/tealeg/xlsx@"} {
		found := false
		for name := range licenses {
			found = found || strings.HasPrefix(name, module)
		}
		if !found {
			t.Fatalf("missing license for %s in %v", module, licenses)
		}
	}
}
