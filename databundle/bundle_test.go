package databundle_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/Coder-is/TabForge/databundle"
	configpb "github.com/Coder-is/TabForge/examples/complete/Generated/schema/go"
	"github.com/Coder-is/TabForge/project"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"
)

func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"Tables", "Protocols"} {
		if err := os.CopyFS(filepath.Join(root, name), os.DirFS(filepath.Join("..", "examples", "complete", name))); err != nil {
			t.Fatal(err)
		}
	}
	config, err := os.ReadFile("../examples/complete/tabforge.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tabforge.json"), config, 0644); err != nil {
		t.Fatal(err)
	}
	p, err := project.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.Export(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Only generated artifacts remain: no runtime compiler or source files needed.
	for _, name := range []string{"Tables", "Protocols", "tabforge.json"} {
		if err := os.RemoveAll(filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	return result.Output
}

func TestSnapshotsTypedMessagesAndReload(t *testing.T) {
	dir := fixture(t)
	var store databundle.Store
	if err := store.Reload(dir, ""); err != nil {
		t.Fatal(err)
	}
	b := store.Snapshot()
	for _, name := range []string{"data/tables.json", "data/tables.pbb", "data/by-table/Item.pbb", "data/tables-map.json"} {
		var typed configpb.Tables
		if err := b.ReadInto(name, &typed); err != nil {
			t.Fatal(err)
		}
		item := typed.Items
		if name == "data/tables-map.json" {
			item = []*configpb.Item{typed.ById[1001]}
		}
		if len(item) == 0 || item[0].OwnerId != ^uint64(0) || item[0].SignedTotal != (-1<<63) || item[0].UnlockLevel == nil || item[0].GetName() != "新手剑😀" {
			t.Fatalf("lost data from %s: %s", name, &typed)
		}
	}
	m, err := b.Read("data/tables.json")
	if err != nil {
		t.Fatal(err)
	}
	proto.Reset(m)
	fresh, err := b.Read("data/tables.json")
	if err != nil || fresh.ProtoReflect().Get(fresh.ProtoReflect().Descriptor().Fields().ByName("items")).List().Len() != 3 {
		t.Fatalf("mutated snapshot: %v", err)
	}
	entries := b.Entries()
	entries[0].Message = "changed"
	if b.Entries()[0].Message == "changed" {
		t.Fatal("mutable metadata")
	}
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for n := 0; n < 10; n++ {
				var dst configpb.Tables
				if err := store.Snapshot().ReadInto("data/tables.json", &dst); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	if err := store.Reload(dir, b.SchemaHash()); err != nil {
		t.Fatal(err)
	}
	group.Wait()
	old := store.Snapshot()
	if err := os.WriteFile(filepath.Join(dir, "data", "tables.json"), []byte(`{"unknown":1}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := store.Reload(dir, ""); err == nil || store.Snapshot() != old {
		t.Fatal("failed reload changed snapshot")
	}
	var unchanged = configpb.Tables{Settings: &configpb.Settings{MaxLevel: 99}}
	if err := b.ReadInto("Missing", &unchanged); err == nil || unchanged.Settings.MaxLevel != 99 {
		t.Fatal("failed typed read mutated target")
	}
	if err := b.ReadInto("data/tables.json", (*configpb.Tables)(nil)); err == nil {
		t.Fatal("nil accepted")
	}
	// Same message name with changed field type must fail before assigning dst.
	set := protodesc.ToFileDescriptorProto((&configpb.Tables{}).ProtoReflect().Descriptor().ParentFile())
	set.MessageType[2].Field[0].Name = proto.String("changed")
	// Resolve the edited file using the original imported descriptors.
	fd, err := protodesc.NewFile(set, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatal(err)
	}
	stale := dynamicpb.NewMessage(fd.Messages().ByName("Tables"))
	if err := b.ReadInto("data/tables.json", stale); err == nil {
		t.Fatal("stale generated type accepted")
	}
}

func TestManifestValidationAndEmbeddedFS(t *testing.T) {
	dir := fixture(t)
	raw, err := os.ReadFile(filepath.Join(dir, databundle.ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	var manifest databundle.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	embedded := fstest.MapFS{}
	for _, name := range append([]string{databundle.ManifestName, manifest.Descriptor.Path, manifest.WireSchema.Path}, dataPaths(manifest)...) {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		embedded["Generated/"+name] = &fstest.MapFile{Data: data}
	}
	if _, err := databundle.OpenFS(embedded, "Generated", manifest.SchemaHash); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*databundle.Manifest){
		func(m *databundle.Manifest) { m.Format = "bad" },
		func(m *databundle.Manifest) { m.SchemaHash = "0" },
		func(m *databundle.Manifest) { m.Data[0].Path = "../outside" },
		func(m *databundle.Manifest) { m.Data[0].Message = "Missing" },
		func(m *databundle.Manifest) { m.Data[0].Encoding = "legacy-bin" },
		func(m *databundle.Manifest) { m.Data = append(m.Data, m.Data[0]) },
	} {
		var next databundle.Manifest
		if err := json.Unmarshal(raw, &next); err != nil {
			t.Fatal(err)
		}
		mutate(&next)
		data, _ := json.Marshal(next)
		if err := os.WriteFile(filepath.Join(dir, databundle.ManifestName), data, 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := databundle.Open(dir, ""); err == nil {
			t.Fatalf("invalid manifest accepted: %s", data)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, databundle.ManifestName), raw, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := databundle.Open(dir, "wrong"); err == nil {
		t.Fatal("expected schema ignored")
	}
	name := filepath.Join(dir, "data", "tables.json")
	backup := name + ".old"
	if err := os.Rename(name, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(backup, name); err == nil {
		if _, err := databundle.Open(dir, ""); err == nil {
			t.Fatal("symlink accepted")
		}
	}
}

func dataPaths(m databundle.Manifest) []string {
	var paths []string
	for _, entry := range m.Data {
		paths = append(paths, entry.Path)
	}
	return paths
}

func TestManifestCaseVariantCannotBypassSymlink(t *testing.T) {
	dir := fixture(t)
	data := filepath.Join(dir, "data")
	if err := os.Rename(data, data+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(data+".old", data); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "DATA")); err != nil {
		t.Skip("case-sensitive filesystem")
	}
	raw, err := os.ReadFile(filepath.Join(dir, databundle.ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.ReplaceAll(string(raw), `"path": "data/`, `"path": "DATA/`))
	if err := os.WriteFile(filepath.Join(dir, databundle.ManifestName), raw, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := databundle.Open(dir, ""); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("case variant read through symlink: %v", err)
	}
}

func exampleCopy(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS("../examples/complete/Generated")); err != nil {
		t.Fatal(err)
	}
	return root
}

// Member names are exact and unknown members are rejected at every manifest
// level, as in every other TabForge loader (encoding/json alone folds case).
func TestManifestMembersAreExact(t *testing.T) {
	dir := exampleCopy(t)
	path := filepath.Join(dir, databundle.ManifestName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, edit := range [][2]string{
		{`"format"`, `"FORMAT"`}, {`"schemaHash"`, `"schemahash"`}, {`"data": [`, `"Data": [`}, {`"format"`, `"extra": 1, "format"`},
		{`"descriptor": {`, `"descriptor": {"size": 1, `}, {`"sha256"`, `"SHA256"`}, {`"encoding"`, `"Encoding"`}, {`"message"`, `"note": "", "message"`},
	} {
		changed := strings.Replace(string(raw), edit[0], edit[1], 1)
		if err := os.WriteFile(path, []byte(changed), 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := databundle.Open(dir, ""); err == nil {
			t.Fatalf("manifest accepted after %q -> %q", edit[0], edit[1])
		}
	}
	if err := os.WriteFile(path, append([]byte("\xef\xbb\xbf"), raw...), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := databundle.Open(dir, ""); err == nil {
		t.Fatal("manifest with a UTF-8 BOM accepted")
	}
	if err := os.WriteFile(path, raw, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := databundle.Open(dir, ""); err != nil {
		t.Fatal(err)
	}
	wire := filepath.Join(dir, "schema", "wire_schema.json")
	schema, err := os.ReadFile(wire)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wire, []byte(strings.Replace(string(schema), `"messages"`, `"Messages"`, 1)), 0644); err != nil {
		t.Fatal(err)
	}
	var m databundle.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if err := databundle.WriteManifest(dir, m.SchemaHash, m.Data); err != nil {
		t.Fatal(err)
	}
	if _, err := databundle.Open(dir, ""); err == nil {
		t.Fatal("wire schema with a misspelled member accepted")
	}
}

// Go 1.23+ reports Windows junctions as ModeIrregular rather than ModeSymlink.
func TestJunctionRejected(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("junctions are a Windows feature")
	}
	dir := exampleCopy(t)
	data := filepath.Join(dir, "data")
	if err := os.Rename(data, data+".old"); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("cmd", "/c", "mklink", "/J", data, data+".old").CombinedOutput(); err != nil {
		t.Skipf("cannot create a junction: %v %s", err, output)
	}
	if _, err := databundle.Open(dir, ""); err == nil || !strings.Contains(err.Error(), "junction") {
		t.Fatalf("junction accepted: %v", err)
	}
}

// Packages exported before wire schemas carried protoName must still load, but
// a schema that drops only some aliases matches neither form.
func TestLegacyWireSchemaWithoutProtoNames(t *testing.T) {
	dir := exampleCopy(t)
	raw, err := os.ReadFile(filepath.Join(dir, databundle.ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	var m databundle.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	wire := filepath.Join(dir, "schema", "wire_schema.json")
	schema, err := os.ReadFile(wire)
	if err != nil {
		t.Fatal(err)
	}
	alias := regexp.MustCompile(`,\s*"protoName": "[a-z0-9_]+"`)
	for _, tc := range []struct {
		limit int
		ok    bool
	}{{-1, true}, {1, false}} {
		n := 0
		edited := alias.ReplaceAllStringFunc(string(schema), func(s string) string {
			if n++; tc.limit >= 0 && n > tc.limit {
				return s
			}
			return ""
		})
		if edited == string(schema) {
			t.Fatal("fixture has no protoName aliases")
		}
		if err := os.WriteFile(wire, []byte(edited), 0644); err != nil {
			t.Fatal(err)
		}
		if err := databundle.WriteManifest(dir, m.SchemaHash, m.Data); err != nil {
			t.Fatal(err)
		}
		if _, err := databundle.Open(dir, ""); (err == nil) != tc.ok {
			t.Fatalf("removed %d aliases: open error %v", tc.limit, err)
		}
	}
}
