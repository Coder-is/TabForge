package tests

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Coder-is/TabForge/protocol"
	"github.com/Coder-is/TabForge/v3/compiler"
	"github.com/Coder-is/TabForge/v3/gen"
	"github.com/Coder-is/TabForge/v3/gen/gosrc"
	"github.com/Coder-is/TabForge/v3/gen/javasrc"
	"github.com/Coder-is/TabForge/v3/gen/pbsrc"
	"github.com/Coder-is/TabForge/v3/helper"
	"github.com/Coder-is/TabForge/v3/model"
)

// sourceActionTable has an indexed field tagged "secret" next to an
// indexed field that stays.
func sourceActionTable(t *testing.T, action string) *TableEmulator {
	emu := NewTableEmulator(t)
	idx := emu.CreateCSVFile("Index")
	helper.WriteIndexTableHeader(idx)
	helper.WriteRowValues(idx, "类型表", "", "Type")
	helper.WriteRowValues(idx, "数据表", "Probe", "Data")
	types := emu.CreateCSVFile("Type")
	helper.WriteRowValues(types, "种类", "对象类型", "标识名", "字段名", "字段类型", "数组切割", "值", "索引", "标记")
	helper.WriteRowValues(types, "表头", "Probe", "ID", "ID", "int32", "", "", "是")
	helper.WriteRowValues(types, "表头", "Probe", "Secret", "Secret", "string", "", "", "是", "secret")
	helper.WriteRowValues(types, "表头", "Probe", "Name", "Name", "string")
	data := emu.CreateCSVFile("Data")
	helper.WriteRowValues(data, "ID", "Secret", "Name")
	helper.WriteRowValues(data, "1", "s", "n")
	if action != "" {
		emu.G.TagActions = []model.TagAction{{Verb: action, Tags: []string{"secret"}}}
	}
	if err := compiler.Compile(emu.G); err != nil {
		t.Fatal(err)
	}
	return emu
}

// nogenfield_go, _java and _proto leave the field, and any index on it,
// out of that language's source and nothing else.
func TestSourceFieldActions(t *testing.T) {
	for _, tc := range []struct {
		name, action string
		generate     gen.GenSingleFile
		check        func(t *testing.T, src []byte)
	}{
		{"Go", model.ActionNoGenFieldGo, gosrc.Generate, nil},
		{"Java", model.ActionNoGenFieldJava, javasrc.Generate, nil},
		{"Proto", model.ActionNoGenFieldProto, pbsrc.Generate, func(t *testing.T, src []byte) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "table.proto"), src, 0644); err != nil {
				t.Fatal(err)
			}
			if _, err := protocol.CompileSchema(context.Background(), []string{dir}, []string{"table.proto"}); err != nil {
				t.Fatalf("filtered proto does not compile: %v\n%s", err, src)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			full, err := tc.generate(sourceActionTable(t, "").G)
			if err != nil {
				t.Fatal(err)
			}
			filtered, err := tc.generate(sourceActionTable(t, tc.action).G)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(full), "Secret") {
				t.Fatalf("unfiltered source lacks the field:\n%s", full)
			}
			if strings.Contains(string(filtered), "Secret") {
				t.Fatalf("%s kept the field:\n%s", tc.action, filtered)
			}
			for _, kept := range []string{"Name", "ProbeByID"} {
				if tc.name == "Proto" && kept == "ProbeByID" {
					continue // Proto has no indices.
				}
				if !strings.Contains(string(filtered), kept) {
					t.Fatalf("%s removed %s:\n%s", tc.action, kept, filtered)
				}
			}
			if tc.check != nil {
				tc.check(t, filtered)
			}
		})
	}
	// Each action applies to its own language only.
	other, err := gosrc.Generate(sourceActionTable(t, model.ActionNoGenFieldJava).G)
	if err != nil || !strings.Contains(string(other), "Secret") {
		t.Fatalf("nogenfield_java changed Go: %v", err)
	}
}
