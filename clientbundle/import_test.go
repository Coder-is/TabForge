package clientbundle

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Coder-is/TabForge/project"
)

func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join("..", "examples", "complete")
	for _, name := range []string{"Tables", "Protocols", "tabforge.json"} {
		if err := filepath.WalkDir(filepath.Join(source, name), func(path string, e os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(source, path)
			if err != nil {
				return err
			}
			dest := filepath.Join(root, rel)
			if e.IsDir() {
				return os.MkdirAll(dest, 0755)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return os.WriteFile(dest, data, 0644)
		}); err != nil {
			t.Fatal(err)
		}
	}
	p, err := project.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	r, err := p.Export(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Imports must work after source files have been discarded.
	for _, name := range []string{"Tables", "Protocols", "tabforge.json"} {
		if err := os.RemoveAll(filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	return r.Output
}
func engine(t *testing.T, kind string) string {
	t.Helper()
	root := t.TempDir()
	switch kind {
	case "unity":
		if err := os.Mkdir(filepath.Join(root, "Assets"), 0755); err != nil {
			t.Fatal(err)
		}
	case "cocos":
		if err := os.Mkdir(filepath.Join(root, "assets"), 0755); err != nil {
			t.Fatal(err)
		}
	case "godot":
		if err := os.WriteFile(filepath.Join(root, "project.godot"), []byte("config_version=5\n"), 0644); err != nil {
			t.Fatal(err)
		}
	case "unreal":
		if err := os.WriteFile(filepath.Join(root, "Game.uproject"), []byte(`{"FileVersion":3}`), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	values := map[string]string{}
	if err := filepath.WalkDir(root, func(path string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !e.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			values[rel] = string(data)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return values
}
func TestPortableImportsAndMetadata(t *testing.T) {
	source := fixture(t)
	for _, kind := range []string{"unity", "cocos", "godot", "unreal"} {
		t.Run(kind, func(t *testing.T) {
			root := engine(t, kind)
			r, err := Import(context.Background(), source, root, kind, true)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(r.Output); !os.IsNotExist(err) {
				t.Fatal("check imported assets")
			}
			r, err = Import(context.Background(), source, root, kind, false)
			if err != nil {
				t.Fatal(err)
			}
			prefix := ""
			if kind == "unity" {
				prefix = "Resources/TabForge/"
			}
			if !strings.Contains(snapshot(t, r.Output)[filepath.FromSlash(prefix+"data/tables.json")], "18446744073709551615") {
				t.Fatal("data missing or precision lost")
			}
			metadata := filepath.Join(r.Output, filepath.FromSlash(prefix+"data/tables.json.meta"))
			if kind == "godot" {
				metadata = filepath.Join(r.Output, "data_schema.gd.uid")
			}
			if err := os.WriteFile(metadata, []byte("stable identity"), 0644); err != nil {
				t.Fatal(err)
			}
			stale := filepath.Join(r.Output, "deleted.json.meta")
			if err := os.WriteFile(stale, []byte("stale"), 0644); err != nil {
				t.Fatal(err)
			}
			if _, err := Import(context.Background(), source, root, kind, false); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(metadata)
			if err != nil || string(data) != "stable identity" {
				t.Fatalf("lost engine identity: %v", err)
			}
			if _, err := os.Stat(stale); !os.IsNotExist(err) {
				t.Fatal("kept stale metadata")
			}
			old := snapshot(t, r.Output)
			file := filepath.Join(source, "data", "tables.json")
			original, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte(`{"unknown":1}`), 0644); err != nil {
				t.Fatal(err)
			}
			if _, err := Import(context.Background(), source, root, kind, false); err == nil {
				t.Fatal("invalid data accepted")
			}
			if !reflect.DeepEqual(old, snapshot(t, r.Output)) {
				t.Fatal("failed import changed assets")
			}
			if err := os.WriteFile(file, original, 0644); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestImportRejectsUnsafeDestinationsAndManifest(t *testing.T) {
	source := fixture(t)
	root := engine(t, "cocos")
	if _, err := Import(context.Background(), source, root, "unknown", false); err == nil {
		t.Fatal("unknown engine accepted")
	}
	out := filepath.Join(root, "assets", "resources", "tabforge")
	if err := os.MkdirAll(out, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "handwritten.ts"), []byte("business code"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(context.Background(), source, root, "cocos", false); err == nil {
		t.Fatal("unmarked assets overwritten")
	}
	file := filepath.Join(source, "export.json")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var manifest project.Result
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Files = append(manifest.Files, "../source.json")
	data, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, data, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(context.Background(), source, engine(t, "unity"), "unity", false); err == nil {
		t.Fatal("escaping manifest accepted")
	}
}

// A data file must never replace runtime files: non-canonical paths bypassed
// the name checks, and case variants alias them on Windows and macOS.
func TestImportRejectsDataAliasingRuntimeFiles(t *testing.T) {
	for _, alias := range []string{"data/../wire_schema.json", "Wire_Schema.json", ".TabForge-client.json"} {
		source := fixture(t)
		file := filepath.Join(source, "export.json")
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var manifest project.Result
		if err := json.Unmarshal(data, &manifest); err != nil {
			t.Fatal(err)
		}
		payload, err := os.ReadFile(filepath.Join(source, "data", "tables.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(source, filepath.Clean(filepath.FromSlash(alias))), payload, 0644); err != nil {
			t.Fatal(err)
		}
		manifest.Files = append(manifest.Files, alias)
		manifest.Data = append(manifest.Data, project.DataFile{Path: alias, Message: manifest.Data[0].Message})
		if data, err = json.Marshal(manifest); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, data, 0644); err != nil {
			t.Fatal(err)
		}
		for _, kind := range []string{"unity", "cocos"} {
			// The alias checks must fire before the checksum check would.
			if _, err := Import(context.Background(), source, engine(t, kind), kind, true); err == nil || strings.Contains(err.Error(), "data_manifest") {
				t.Errorf("%s: data path %q accepted", kind, alias)
			}
		}
	}
}

// databundle.Open rejects data whose bytes differ from data_manifest.json, so
// the engine import must too, even when the swapped file is valid ProtoJSON.
func TestImportVerifiesDataManifestChecksums(t *testing.T) {
	source := fixture(t)
	other, err := os.ReadFile(filepath.Join(source, "data", "tables-map.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "data", "tables.json"), other, 0644); err != nil {
		t.Fatal(err)
	}
	for _, check := range []bool{true, false} {
		root := engine(t, "cocos")
		_, err := Import(context.Background(), source, root, "cocos", check)
		if err == nil || !strings.Contains(err.Error(), "checksum mismatch") || !strings.Contains(err.Error(), "data/tables.json") {
			t.Fatalf("check=%v: swapped data accepted or unclear error: %v", check, err)
		}
		if _, err := os.Stat(filepath.Join(root, "assets", "resources", "tabforge")); !os.IsNotExist(err) {
			t.Fatalf("check=%v: assets written", check)
		}
	}
}

// A crashed import must neither block later imports nor lose the last good assets.
func TestImportRecoversInterruptedPublishAndStaleLock(t *testing.T) {
	source := fixture(t)
	root := engine(t, "godot")
	r, err := Import(context.Background(), source, root, "godot", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.Output, "schema.gd.uid"), []byte("uid://kept"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(r.Output, r.Output+".tabforge-backup"); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	host, _ := os.Hostname()
	lock := filepath.Join(root, ".tabforge-client.lock")
	if err := os.WriteFile(lock, fmt.Appendf(nil, `{"pid":%d,"host":%q,"started":"2026-01-01T00:00:00Z"}`, cmd.Process.Pid, host), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(context.Background(), source, root, "godot", false); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(r.Output, "schema.gd.uid")); err != nil || string(data) != "uid://kept" {
		t.Fatalf("restored metadata lost: %v", err)
	}
	for _, name := range []string{r.Output + ".tabforge-backup", lock} {
		if _, err := os.Lstat(name); !os.IsNotExist(err) {
			t.Fatalf("%s left behind: %v", name, err)
		}
	}
}
