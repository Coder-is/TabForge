package databundle_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Coder-is/TabForge/databundle"
	configpb "github.com/Coder-is/TabForge/examples/complete/Generated/schema/go"
)

// The shared wire cases record Go protojson's behavior; every SDK replays them.
func TestSharedWireCases(t *testing.T) {
	b, err := databundle.Open("../examples/complete/Generated", "")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../examples/backend/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Message string `json:"message"`
		Valid   bool   `json:"valid"`
		JSON    string `json:"json"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for i, c := range cases {
		_, err := b.DecodeJSON(c.Message, []byte(c.JSON))
		if (err == nil) != c.Valid {
			t.Errorf("case %d %s %s: valid=%v err=%v", i, c.Message, c.JSON, c.Valid, err)
		}
	}
}

// largeTable is the shared large-table fixture (sdk/typescript/node-bundle.test.mjs
// writes the same bytes): 20,000 rows and 180,002 JSON values, well above the
// 100,000-value network message budget but inside the data bundle budget.
func largeTable() []byte {
	var b strings.Builder
	b.WriteString(`{"items":[`)
	for i := 1; i <= 20000; i++ {
		if i > 1 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"id":%d,"name":"item %d","ownerId":"%d","levels":[1,2,3],"price":1.5}`, i, i, i)
	}
	b.WriteString("]}")
	return []byte(b.String())
}

func TestLargeDataFileUsesDataBudget(t *testing.T) {
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS("../examples/complete/Generated")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "data", "tables.json"), largeTable(), 0644); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, databundle.ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	var m databundle.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if err := databundle.WriteManifest(root, m.SchemaHash, m.Data); err != nil {
		t.Fatal(err)
	}
	b, err := databundle.Open(root, "")
	if err != nil {
		t.Fatal(err)
	}
	var tables configpb.Tables
	if err := b.ReadInto("data/tables.json", &tables); err != nil {
		t.Fatal(err)
	}
	if len(tables.Items) != 20000 || tables.Items[19999].GetOwnerId() != 20000 {
		t.Fatalf("large table decoded wrongly: %d rows", len(tables.Items))
	}
}

func TestCheckBudgetBoundaries(t *testing.T) {
	nested := func(depth int) []byte {
		return []byte(strings.Repeat("[", depth) + "0" + strings.Repeat("]", depth))
	}
	if err := databundle.CheckBudget(nested(databundle.MaxJSONDepth)); err != nil {
		t.Fatal(err)
	}
	if databundle.CheckBudget(nested(databundle.MaxJSONDepth+1)) == nil {
		t.Fatal("accepted depth 65")
	}
	if err := databundle.CheckBudget([]byte(`{"a":{"b":[1,{"c":null}]},"d":"x"}`)); err != nil {
		t.Fatal(err)
	}
	// One array plus N-1 zeros is exactly N values.
	values := func(n int) []byte {
		return append(append([]byte("["), bytes.Repeat([]byte("0,"), n-2)...), "0]"...)
	}
	if err := databundle.CheckBudget(values(databundle.MaxJSONNodes)); err != nil {
		t.Fatal(err)
	}
	if databundle.CheckBudget(values(databundle.MaxJSONNodes+1)) == nil {
		t.Fatal("accepted more than MaxJSONNodes values")
	}
}
