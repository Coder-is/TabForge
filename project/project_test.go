package project

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
		`{"version":1,"schema":{},"tables":[{"index":"Tables/Index.csv","outputs":{"json":"data_manifest.json"}}]}`,
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

// A crash during publishing or a failed backup cleanup must not block later
// exports, and the last good output must survive.
func TestExportRecoversInterruptedPublishAndStaleLock(t *testing.T) {
	root := copyFixture(t)
	p, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	r, err := p.Export(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	previous := snapshot(t, r.Output)
	backup := r.Output + ".tabforge-backup"
	// Backup cleanup failed after the new output was installed.
	if err := os.MkdirAll(filepath.Join(backup, "data"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backup, "data", "stale.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Export(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(backup); !os.IsNotExist(err) {
		t.Fatalf("superseded backup kept: %v", err)
	}
	// Crash between moving the old output aside and installing the new one.
	if err := os.Rename(r.Output, backup); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(r.Output); !os.IsNotExist(err) {
		t.Fatal("check changed the published output")
	}
	// A crashed exporter on this host also left its lock behind.
	dead := deadPID(t)
	host, _ := os.Hostname()
	lock := filepath.Join(root, ".tabforge-export.lock")
	if err := os.WriteFile(lock, fmt.Appendf(nil, `{"pid":%d,"host":%q,"started":"2026-01-01T00:00:00Z"}`, dead, host), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Export(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(previous, snapshot(t, r.Output)) {
		t.Fatal("recovered export differs from the last good output")
	}
	for _, name := range []string{backup, lock} {
		if _, err := os.Lstat(name); !os.IsNotExist(err) {
			t.Fatalf("%s left behind: %v", name, err)
		}
	}
	// A lock held by this live process is refused with its owner.
	if err := os.WriteFile(lock, fmt.Appendf(nil, `{"pid":%d,"host":%q,"started":"2026-01-01T00:00:00Z"}`, os.Getpid(), host), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Export(context.Background()); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("pid %d", os.Getpid())) {
		t.Fatalf("live lock: %v", err)
	}
}

func deadPID(t *testing.T) int {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

// Windows resolves "Protocols." and the 8.3 name "PROTOC~1" to Protocols; an
// export to either spelling previously replaced the Proto sources.
func TestOutputAliasesCannotReplaceSources(t *testing.T) {
	root := copyFixture(t)
	before := snapshot(t, filepath.Join(root, "Protocols"))
	aliases := []string{"Protocols.", "Protocols ", "Protocols/x."}
	if _, err := os.Stat(filepath.Join(root, "PROTOC~1")); err == nil {
		aliases = append(aliases, "PROTOC~1")
	}
	for _, output := range aliases {
		p, err := Load(root)
		if err != nil {
			t.Fatal(err)
		}
		p.Config.Output, p.Config.Tables, p.Config.Protocol = output, nil, ""
		if _, err := p.Export(context.Background()); err == nil {
			t.Errorf("output %q accepted", output)
		}
		if !reflect.DeepEqual(before, snapshot(t, filepath.Join(root, "Protocols"))) {
			t.Fatalf("output %q replaced sources", output)
		}
	}
	p, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	p.Config.Tables[0].Outputs = map[string]string{}
	if err := p.validate(); err == nil || !strings.Contains(err.Error(), "Tables: no outputs") {
		t.Fatalf("discover job error lost its directory: %v", err)
	}
}
