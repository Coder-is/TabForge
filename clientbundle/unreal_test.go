package clientbundle

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestUnrealImportAssociatesMessagesAndValidatesProject(t *testing.T) {
	source := fixture(t)
	root := engine(t, "unreal")
	r, err := Import(context.Background(), source, root, "unreal", false)
	if err != nil {
		t.Fatal(err)
	}
	if r.Output != filepath.Join(root, "Content", "TabForgeGenerated") {
		t.Fatal(r.Output)
	}
	var bundle struct {
		Format     string                           `json:"format"`
		SchemaHash string                           `json:"schemaHash"`
		Data       []struct{ Path, Message string } `json:"data"`
	}
	raw, err := os.ReadFile(filepath.Join(r.Output, "bundle.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &bundle); err != nil {
		t.Fatal(err)
	}
	if bundle.Format != "tabforge.unreal.data.v1" || bundle.SchemaHash != r.SchemaHash || len(bundle.Data) != 2 {
		t.Fatalf("bad Unreal bundle: %s", raw)
	}
	if bundle.Data[0].Message != "tabforge.demo.config.Tables" {
		t.Fatal("missing root message")
	}
	if _, err := Import(context.Background(), source, t.TempDir(), "unreal", false); err == nil {
		t.Fatal("accepted non-Unreal project")
	}
}

func TestUnrealUsesCanonicalValidationHeaders(t *testing.T) {
	for _, file := range []string{"TabForgeWire.h", "TabForgeJsonSyntax.h", "TabForgeSse.h"} {
		scope := "Public"
		if file == "TabForgeWire.h" {
			scope = "Private"
		}
		canonical, err := os.ReadFile(filepath.Join("..", "sdk", "unreal", "Source", "TabForgeProtocol", scope, file))
		if err != nil {
			t.Fatal(err)
		}
		copy, err := os.ReadFile(filepath.Join("..", "editors", "unreal", "Source", "TabForgeData", "Private", file))
		if err != nil {
			t.Fatal(err)
		}
		if string(canonical) != string(copy) {
			t.Fatalf("Unreal data validator forked from shared core: %s", file)
		}
	}
}
