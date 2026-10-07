package project

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDropInFilesAndDeletionWithoutIndex(t *testing.T) {
	root := copyFixture(t)
	p, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Tables/Index.csv", "Tables/Basic/Index.csv"} {
		if err := os.Remove(filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	first, err := p.Export(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	indexBefore := snapshot(t, filepath.Join(root, "Tables"))
	header, err := os.ReadFile(filepath.Join(root, "Tables/ItemsExtra.csv"))
	if err != nil {
		t.Fatal(err)
	}
	newFile := filepath.Join(root, "Tables/ItemsExtraNew.csv")
	raw := strings.Replace(string(header), "1003,", "1004,", 1)
	if err := os.WriteFile(newFile, []byte(raw), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Export(context.Background()); err != nil {
		t.Fatal(err)
	}
	var tables struct {
		Items []map[string]any `json:"items"`
	}
	data, _ := os.ReadFile(filepath.Join(first.Output, "data/tables.json"))
	if err := json.Unmarshal(data, &tables); err != nil {
		t.Fatal(err)
	}
	if len(tables.Items) != 4 {
		t.Fatalf("new file not discovered: %s", data)
	}
	if err := os.Remove(newFile); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Export(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(filepath.Join(first.Output, "data/tables.json"))
	if err := json.Unmarshal(data, &tables); err != nil {
		t.Fatal(err)
	}
	if len(tables.Items) != 3 {
		t.Fatal("deleted file remains in output")
	}
	if !reflect.DeepEqual(indexBefore, snapshot(t, filepath.Join(root, "Tables"))) {
		t.Fatal("export rewrote source/index files")
	}
}

func TestDiscoveryRejectsAmbiguityUnknownFilesAndPreservesOutput(t *testing.T) {
	root := copyFixture(t)
	p, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	r, err := p.Export(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, r.Output)
	for _, filename := range []string{"Unknown.csv", "ItemsExtraDuplicate.csv"} {
		input := filepath.Join(root, "Tables", filename)
		data, _ := os.ReadFile(filepath.Join(root, "Tables/ItemsExtra.csv"))
		if err := os.WriteFile(input, data, 0644); err != nil {
			t.Fatal(err)
		}
		_, exportErr := p.Export(context.Background())
		if exportErr == nil {
			t.Fatalf("accepted %s", filename)
		}
		if filename == "Unknown.csv" {
			diagnostics := p.Diagnose(exportErr)
			if len(diagnostics) != 1 || diagnostics[0].Code != "discovery_input" || diagnostics[0].Path != input || diagnostics[0].Hint == "" {
				t.Fatalf("discovery report does not identify the input: %+v", diagnostics)
			}
		}
		if !reflect.DeepEqual(before, snapshot(t, r.Output)) {
			t.Fatal("failed discovery/compile changed output")
		}
		if err := os.Remove(input); err != nil {
			t.Fatal(err)
		}
	}
	d := p.Config.Tables[0].Discover
	d.Rules = append(d.Rules, DiscoverRule{Pattern: "ItemsExtra*.csv", Type: "Item"})
	if _, err := p.Export(context.Background()); err == nil {
		t.Fatal("ambiguous rule accepted")
	}
	if !reflect.DeepEqual(before, snapshot(t, r.Output)) {
		t.Fatal("ambiguity replaced output")
	}
}

func TestDiscoveryPatternsAndIgnoredFiles(t *testing.T) {
	for _, test := range []struct {
		p, n string
		want bool
	}{{"Item/**/*.csv", "Item/a.csv", true}, {"Item/**/*.csv", "Item/event/a.csv", true}, {"Item/*.csv", "Item/event/a.csv", false}, {"Archive/**", "Archive", true}} {
		if matchPattern(test.p, test.n) != test.want {
			t.Fatalf("%s / %s", test.p, test.n)
		}
	}
	for _, bad := range []string{"../*.csv", "/a", "a\\b", "a/**b", "[bad", "a//b"} {
		if validatePattern(bad) == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	root := copyFixture(t)
	p, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Tables/~$Items.xlsx"), []byte("not an XLSX"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Tables/.draft.csv"), []byte("draft"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Export(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyIndexStillWorks(t *testing.T) {
	root := copyFixture(t)
	p, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	for i := range p.Config.Tables {
		dir := p.Config.Tables[i].Discover.Dir
		p.Config.Tables[i].Discover = nil
		p.Config.Tables[i].Index = dir + "/Index.csv"
	}
	if _, err := p.Export(context.Background()); err != nil {
		t.Fatal(err)
	}
}
