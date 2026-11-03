package publish

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func output(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"export.json", name} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestDirRollsBackFailedInstall(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "Generated")
	output(t, out, "old", "previous")
	if _, err := Dir(filepath.Join(dir, "missing-stage"), out, "export.json"); err == nil {
		t.Fatal("missing stage accepted")
	}
	if read(t, filepath.Join(out, "old")) != "previous" {
		t.Fatal("rollback lost output")
	}
	if _, err := os.Lstat(out + BackupSuffix); !os.IsNotExist(err) {
		t.Fatal("backup left after rollback")
	}
}

func TestDirRemovesSupersededBackup(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "Generated")
	output(t, out, "new", "current")
	output(t, out+BackupSuffix, "old", "superseded")
	stage := filepath.Join(dir, "stage")
	output(t, stage, "next", "next")
	if warning, err := Dir(stage, out, "export.json"); err != nil || warning != nil {
		t.Fatal(warning, err)
	}
	if read(t, filepath.Join(out, "next")) != "next" {
		t.Fatal("stage not installed")
	}
	if _, err := os.Lstat(out + BackupSuffix); !os.IsNotExist(err) {
		t.Fatal("superseded backup kept")
	}
}

func TestRecoverRestoresInterruptedSwap(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "Generated")
	output(t, out+BackupSuffix, "old", "last good")
	// A failed install must still leave the last good output in place.
	if _, err := Dir(filepath.Join(dir, "missing-stage"), out, "export.json"); err == nil {
		t.Fatal("missing stage accepted")
	}
	if read(t, filepath.Join(out, "old")) != "last good" {
		t.Fatal("backup not restored")
	}
	if _, err := os.Lstat(out + BackupSuffix); !os.IsNotExist(err) {
		t.Fatal("backup kept after restore")
	}
}

func TestRecoverKeepsBackupBesideUnknownDirectory(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "Generated")
	if err := os.Mkdir(out, 0755); err != nil {
		t.Fatal(err)
	}
	output(t, out+BackupSuffix, "old", "last good")
	if err := Recover(out, "export.json"); err == nil {
		t.Fatal("unmarked output accepted")
	}
	if read(t, filepath.Join(out+BackupSuffix, "old")) != "last good" {
		t.Fatal("backup removed")
	}
}

func TestDirRefusesUnmarkedOutput(t *testing.T) {
	dir := t.TempDir()
	docs := filepath.Join(dir, "Docs")
	if err := os.Mkdir(docs, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(docs, "design.md"), []byte("handwritten"), 0644); err != nil {
		t.Fatal(err)
	}
	readme := filepath.Join(dir, "README.md")
	if err := os.WriteFile(readme, []byte("handwritten"), 0644); err != nil {
		t.Fatal(err)
	}
	// A directory named like the marker is not a marker.
	fake := filepath.Join(dir, "Fake")
	if err := os.MkdirAll(filepath.Join(fake, "export.json"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{docs, readme, fake} {
		stage := filepath.Join(dir, "stage")
		output(t, stage, "next", "next")
		if _, err := Dir(stage, out, "export.json"); err == nil || !strings.Contains(err.Error(), "refusing to replace") {
			t.Fatalf("%s: %v", out, err)
		}
		if _, err := os.Lstat(out + BackupSuffix); !os.IsNotExist(err) {
			t.Fatalf("%s: moved aside", out)
		}
		os.RemoveAll(stage)
	}
	if read(t, filepath.Join(docs, "design.md")) != "handwritten" || read(t, readme) != "handwritten" {
		t.Fatal("handwritten file changed")
	}
	// An empty directory holds nothing to lose and is replaced.
	empty := filepath.Join(dir, "Empty")
	if err := os.Mkdir(empty, 0755); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(dir, "stage")
	output(t, stage, "next", "next")
	if _, err := Dir(stage, empty, "export.json"); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(empty, "next")) != "next" {
		t.Fatal("empty directory not replaced")
	}
}
