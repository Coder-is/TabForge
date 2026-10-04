package project

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Coder-is/TabForge/protocol"
)

func copyFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join("..", "examples", "complete")
	for _, name := range []string{"Tables", "Protocols", "tabforge.json"} {
		err := filepath.WalkDir(filepath.Join(source, name), func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(source, path)
			if err != nil {
				return err
			}
			dest := filepath.Join(root, rel)
			if entry.IsDir() {
				return os.MkdirAll(dest, 0755)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return os.WriteFile(dest, data, 0644)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		result[rel] = string(data)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestCompleteProjectPortableAndTransactional(t *testing.T) {
	root := copyFixture(t)
	p, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	// No PATH tools are available; compilation/generation must be in-process.
	t.Setenv("PATH", t.TempDir())
	r, err := p.Export(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	previous := snapshot(t, r.Output)
	schema, err := protocol.LoadSchema(filepath.Join(r.Output, "schema", "schema.pb"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := schema.ReadFile("tabforge.demo.config.Tables", filepath.Join(r.Output, "data", "tables.pbb")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(r.Output, "data", "tables.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Items) != 3 || config.Items[0]["ownerId"] != "18446744073709551615" || config.Items[0]["signedTotal"] != "-9223372036854775808" {
		t.Fatalf("precision/merge: %s", data)
	}
	if _, present := config.Items[0]["unlockLevel"]; !present {
		t.Fatal("lost optional zero")
	}
	if _, present := config.Items[1]["unlockLevel"]; present {
		t.Fatal("empty optional was assigned")
	}
	if _, err := protocol.Load(filepath.Join(r.Output, "protocol", "contract.json")); err != nil {
		t.Fatal(err)
	}
	basic := previous[filepath.Join("basic", "tables.json")]
	if strings.Contains(basic, "ServerNote") || !strings.Contains(basic, "Welcome") {
		t.Fatalf("tag/KV: %s", basic)
	}
	if _, err := p.Export(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(previous, snapshot(t, r.Output)) {
		t.Fatal("repeated generation is not deterministic")
	}
	// A failure in the last table job must not publish earlier successful outputs.
	bad := filepath.Join(root, "Tables", "Basic", "ActorsExtra.csv")
	data, err = os.ReadFile(bad)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte(strings.Replace(string(data), "3,合并", "1,合并", 1)), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Export(context.Background()); err == nil {
		t.Fatal("duplicate ID accepted")
	}
	if !reflect.DeepEqual(previous, snapshot(t, r.Output)) {
		t.Fatal("failed export changed previous bundle")
	}
	if err := os.WriteFile(bad, data, 0644); err != nil {
		t.Fatal(err)
	}
	// Cancellation must also preserve the previous bundle.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Export(ctx); err == nil {
		t.Fatal("cancelled export succeeded")
	}
	if !reflect.DeepEqual(previous, snapshot(t, r.Output)) {
		t.Fatal("cancelled export published output")
	}
	lock := filepath.Join(root, ".tabforge-export.lock")
	if err := os.WriteFile(lock, []byte("other process"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Export(context.Background()); err == nil {
		t.Fatal("concurrent export accepted")
	}
}

func TestProjectConfigRejectsUnsafeAndConflictingPaths(t *testing.T) {
	for _, config := range []string{
		`{"version":1,"unknown":true,"schema":{}}`,
		`{"version":1,"output":"../outside","schema":{}}`,
		`{"version":1,"output":"Tables","tables":[{"index":"Tables/Index.csv","outputs":{"json":"a.json"}}]}`,
		`{"version":1,"schema":{},"tables":[{"index":"Tables/Index.csv","outputs":{"json":"schema/types.ts"}}]}`,
		`{"version":1,"tables":[{"index":"Tables/Index.csv","outputs":{"json":"a","json_dir":"a/b"}}]}`,
		`{"version":1,"tables":[{"index":"Tables/Index.csv","outputs":{"json":"a.json","lua":"A.json"}}]}`,
		`{"version":1,"tables":[{"index":"Tables/Index.csv","outputs":{"json":"../escaped.json"}}]}`,
		`{"version":1,"tables":[{"index":"Tables/Index.csv","outputs":{"protojson":"a.json"}}]}`,
		`{"version":1,"schema":{"files":["../bad.proto"]}}`,
		`{"version":1,"protocol":"Protocols/contract.json"}`,
	} {
		dir := t.TempDir()
		file := filepath.Join(dir, "tabforge.json")
		if err := os.WriteFile(file, []byte(config), 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(file); err == nil {
			t.Errorf("accepted %s", config)
		}
	}
}

func TestPureSchemaProjectAndDiscovery(t *testing.T) {
	root := copyFixture(t)
	p, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	p.Config.Protocol = ""
	p.Config.Tables = nil
	result, err := p.Export(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(result.Output, "protocol")); !os.IsNotExist(err) {
		t.Fatal("pure data generated network bundle")
	}
	if got, err := Find(filepath.Join(root, "Tables", "Basic")); err != nil || got != filepath.Join(root, "tabforge.json") {
		t.Fatalf("discovery: %s %v", got, err)
	}
}

func TestPublishRollsBackAndKeepsStaleBackup(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "Generated")
	if err := os.Mkdir(out, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "old"), []byte("previous"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := publish(filepath.Join(dir, "nonexistent-stage"), out); err == nil {
		t.Fatal("missing stage accepted")
	}
	if data, err := os.ReadFile(filepath.Join(out, "old")); err != nil || string(data) != "previous" {
		t.Fatalf("rollback lost output: %v", err)
	}
	if err := os.Mkdir(out+".tabforge-backup", 0755); err != nil {
		t.Fatal(err)
	}
	if err := publish(filepath.Join(dir, "nonexistent-stage"), out); err == nil {
		t.Fatal("stale backup silently replaced")
	}
	if _, err := os.Stat(filepath.Join(out, "old")); err != nil {
		t.Fatal(err)
	}
}
