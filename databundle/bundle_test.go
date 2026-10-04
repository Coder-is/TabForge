package databundle_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
