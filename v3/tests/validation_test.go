package tests

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Coder-is/TabForge/v3/compiler"
	"github.com/Coder-is/TabForge/v3/gen"
	"github.com/Coder-is/TabForge/v3/gen/cssrc"
	"github.com/Coder-is/TabForge/v3/gen/gosrc"
	"github.com/Coder-is/TabForge/v3/gen/javasrc"
	"github.com/Coder-is/TabForge/v3/gen/jsondata"
	"github.com/Coder-is/TabForge/v3/gen/luasrc"
	"github.com/Coder-is/TabForge/v3/gen/pbdata"
	"github.com/Coder-is/TabForge/v3/gen/pbsrc"
	"github.com/Coder-is/TabForge/v3/helper"
	"github.com/Coder-is/TabForge/v3/model"
	"github.com/Coder-is/TabForge/v3/report"
)

func requireTableError(t *testing.T, err error, id string) {
	t.Helper()
	te, ok := err.(*report.TableError)
	if !ok || te.ID != id {
		t.Fatalf("expected %s, got %v", id, err)
	}
}

func (emu *TableEmulator) sheet(name string) helper.TableSheet {
	emu.T.Helper()
	file, err := emu.GetFile(name)
	if err != nil {
		emu.T.Fatal(err)
	}
	return file.Sheets()[0]
}

func indexedTable(t *testing.T, kind, splitter string, values ...string) *TableEmulator {
	t.Helper()
	emu := NewTableEmulator(t)
	index := emu.CreateCSVFile("Index")
	helper.WriteIndexTableHeader(index)
	helper.WriteRowValues(index, "类型表", "", "Type")
	helper.WriteRowValues(index, "数据表", "Probe", "Data")
	types := emu.CreateCSVFile("Type")
	helper.WriteTypeTableHeader(types)
	helper.WriteRowValues(types, "表头", "Probe", "ID", "ID", kind, splitter, "", "是")
	helper.WriteRowValues(types, "表头", "Probe", "Marker", "Marker", "string")
	data := emu.CreateCSVFile("Data")
	helper.WriteRowValues(data, "ID", "Marker")
	for i, value := range values {
		// A non-indexed value keeps blank index cells from terminating the table.
		helper.WriteRowValues(data, value, fmt.Sprint(i))
	}
	return emu
}

func TestUint16JSONBoundaries(t *testing.T) {
	emu := indexedTable(t, "uint16", "", "0", "32767", "32768", "65535")
	types := emu.sheet("Type")
	helper.WriteRowValues(types, "表头", "Probe", "Values", "Values", "uint16", "|")
	data := emu.CreateCSVFile("Data")
	helper.WriteRowValues(data, "ID", "Marker", "Values")
	for i, value := range []string{"0", "32767", "32768", "65535"} {
		helper.WriteRowValues(data, value, fmt.Sprint(i), "32767|32768|65535")
	}
	if err := compiler.Compile(emu.G); err != nil {
		t.Fatal(err)
	}
	verify := func(data []byte) {
		var output struct {
			Probe []struct {
				ID     uint16
				Values []uint16
			}
		}
		if err := json.Unmarshal(data, &output); err != nil {
			t.Fatal(err)
		}
		if len(output.Probe) != 4 {
			t.Fatalf("missing rows: %s", data)
		}
		for i, value := range []uint16{0, 32767, 32768, 65535} {
			if output.Probe[i].ID != value || !reflect.DeepEqual(output.Probe[i].Values, []uint16{32767, 32768, 65535}) {
				t.Fatalf("uint16 values changed: %s", data)
			}
		}
	}
	all, err := jsondata.Generate(emu.G)
	if err != nil {
		t.Fatal(err)
	}
	verify(all)
	dir, err := ioutil.TempDir("", "tabforge-json-boundaries-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if err := jsondata.Output(emu.G, dir); err != nil {
		t.Fatal(err)
	}
	part, err := ioutil.ReadFile(filepath.Join(dir, "Probe.json"))
	if err != nil {
		t.Fatal(err)
	}
	verify(part)
}

func TestTypedIndexDuplicates(t *testing.T) {
	for _, tc := range []struct {
		name, kind, first, second string
	}{
		{"signed", "int16", "1", "+1"},
		{"integer alias", "int", "1", "01"},
		{"unsigned", "uint64", "18446744073709551615", "018446744073709551615"},
		{"float", "double", "1", "1e0"},
		{"float32 rounding", "float", "1", "1.00000001"},
		{"negative zero", "float64", "-0", "0"},
		{"boolean", "bool", "true", "是"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			emu := indexedTable(t, tc.kind, "", tc.first, tc.second)
			requireTableError(t, compiler.Compile(emu.G), "DuplicateValueInMakingIndex")
		})
	}
	t.Run("enum aliases", func(t *testing.T) {
		emu := indexedTable(t, "State", "", "就绪", "Ready")
		types := emu.sheet("Type")
		helper.WriteRowValues(types, "枚举", "State", "", "None", "int32", "", "0")
		helper.WriteRowValues(types, "枚举", "State", "就绪", "Ready", "int32", "", "1")
		requireTableError(t, compiler.Compile(emu.G), "DuplicateValueInMakingIndex")
	})
	t.Run("missing split column defaults", func(t *testing.T) {
		emu := indexedTable(t, "int32", "", "0")
		helper.WriteRowValues(emu.sheet("Index"), "数据表", "Probe", "Other")
		other := emu.CreateCSVFile("Other")
		helper.WriteRowValues(other, "Marker")
		helper.WriteRowValues(other, "another")
		if err := compiler.Compile(emu.G); err != nil {
			t.Fatal(err)
		}
	})
}

func TestDistinctIndicesAndInvalidInput(t *testing.T) {
	for _, tc := range []struct {
		kind, splitter string
		values         []string
		id             string
	}{
		{"string", "", []string{"1", "01"}, ""},
		{"int32", "", []string{"", "0", ""}, ""},
		{"bool", "", []string{"", "false", ""}, ""},
		{"string", "", []string{"", ""}, ""},
		{"uint16", "", []string{"32767", "32768", "65535"}, ""},
		{"int64", "", []string{"9007199254740992", "9007199254740993"}, ""},
		{"uint16", "", []string{"-1"}, "DataMissMatchTypeDefine"},
		{"uint16", "", []string{"65536"}, "DataMissMatchTypeDefine"},
		{"int32", "", []string{"bad", "bad"}, "DataMissMatchTypeDefine"},
		{"float64", "", []string{"NaN"}, "InvalidIndexType"},
		{"float64", "", []string{"+Inf"}, "InvalidIndexType"},
		{"int32", "|", []string{"1|2"}, "InvalidIndexType"},
	} {
		t.Run(tc.kind+strings.Join(tc.values, "/"), func(t *testing.T) {
			emu := indexedTable(t, tc.kind, tc.splitter, tc.values...)
			err := compiler.Compile(emu.G)
			if tc.id == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				requireTableError(t, err, tc.id)
			}
		})
	}
}

func TestTypeNamesAndAliases(t *testing.T) {
	for _, tc := range []struct {
		name, id string
		rows     [][]string
	}{
		{"enum then struct", "ConflictingTypeName", [][]string{{"枚举", "Mixed", "", "None", "int32", "", "0"}, {"表头", "Mixed", "Value", "Value", "int32"}}},
		{"struct then enum", "ConflictingTypeName", [][]string{{"表头", "Mixed", "Value", "Value", "int32"}, {"枚举", "Mixed", "", "None", "int32", "", "0"}}},
		{"primitive type", "ConflictingTypeName", [][]string{{"表头", "int32", "Value", "Value", "int32"}}},
		{"builtin type", "ConflictingTypeName", [][]string{{"表头", "TypeDefine", "Extra", "Extra", "int32"}}},
		{"duplicate alias", "AmbiguousTypeFieldName", [][]string{{"表头", "Probe", "同名", "First", "string"}, {"表头", "Probe", "同名", "Second", "string"}}},
		{"alias matches prior field", "AmbiguousTypeFieldName", [][]string{{"表头", "Probe", "名称", "First", "string"}, {"表头", "Probe", "First", "Second", "string"}}},
		{"field matches prior alias", "AmbiguousTypeFieldName", [][]string{{"表头", "Probe", "Second", "First", "string"}, {"表头", "Probe", "名称", "Second", "string"}}},
		{"same alias as own field", "", [][]string{{"表头", "Probe", "First", "First", "string"}, {"表头", "Probe", "Second", "Second", "string"}}},
		{"empty aliases", "", [][]string{{"枚举", "State", "", "None", "int32", "", "0"}, {"枚举", "State", "", "Ready", "int32", "", "1"}}},
		{"aliases scoped to object", "", [][]string{{"表头", "First", "名称", "Name", "string"}, {"表头", "Second", "名称", "Name", "string"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			emu := NewTableEmulator(t)
			idx := emu.CreateCSVFile("Index")
			helper.WriteIndexTableHeader(idx)
			// Exercise checks across separate type files as well.
			for i, row := range tc.rows {
				name := fmt.Sprintf("Type%d", i)
				helper.WriteRowValues(idx, "类型表", "", name)
				types := emu.CreateCSVFile(name)
				helper.WriteTypeTableHeader(types)
				helper.WriteRowValues(types, row...)
			}
			err := compiler.Compile(emu.G)
			if tc.id == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				requireTableError(t, err, tc.id)
				if !strings.Contains(err.Error(), "@Type") && !strings.Contains(err.Error(), "-combinename") {
					t.Fatalf("missing error location: %v", err)
				}
			}
		})
	}
}

func TestKVNameValidation(t *testing.T) {
	for _, tc := range []struct{ name, object, firstAlias, secondAlias, id string }{
		{"root collision on output only", "Table", "First", "Second", ""},
		{"alias collision", "Config", "名称", "名称", "AmbiguousTypeFieldName"},
		{"reverse alias", "Config", "Second", "名称", "AmbiguousTypeFieldName"},
		{"enum collision", "State", "First", "Second", "ConflictingTypeName"},
		{"valid", "Config", "First", "Second", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			emu := NewTableEmulator(t)
			idx := emu.CreateCSVFile("Index")
			helper.WriteIndexTableHeader(idx)
			helper.WriteRowValues(idx, "键值表", tc.object, "KV")
			kv := emu.CreateCSVFile("KV")
			helper.WriteRowValues(kv, "字段名", "字段类型", "标识名", "值")
			helper.WriteRowValues(kv, "First", "string", tc.firstAlias, "a")
			helper.WriteRowValues(kv, "Second", "string", tc.secondAlias, "b")
			if tc.object == "State" {
				helper.WriteRowValues(idx, "类型表", "", "Type")
				types := emu.CreateCSVFile("Type")
				helper.WriteTypeTableHeader(types)
				helper.WriteRowValues(types, "枚举", "State", "", "None", "int32", "", "0")
			}
			err := compiler.Compile(emu.G)
			if tc.id == "" {
				if err != nil {
					t.Fatal(err)
				}
				if tc.object == "Table" {
					if _, err := cssrc.Generate(emu.G); err == nil || !strings.Contains(err.Error(), "conflict") {
						t.Fatalf("root collision wasn't caught: %v", err)
					}
				}
			} else {
				requireTableError(t, err, tc.id)
			}
		})
	}
}

func TestOutputNameValidation(t *testing.T) {
	for _, tc := range []struct {
		name, object, field, pkg, root, want string
		generate                             gen.GenSingleFile
	}{
		{"C# keyword", "Probe", "class", "main", "Table", "invalid csharp", cssrc.Generate},
		{"Java keyword", "Probe", "native", "main", "Table", "invalid java", javasrc.Generate},
		{"Java restricted type", "record", "ID", "main", "Table", "invalid java", javasrc.Generate},
		{"Lua keyword", "Probe", "end", "main", "Table", "invalid lua", luasrc.Generate},
		{"Proto Unicode", "Probe", "中文", "main", "Table", "invalid protobuf", pbsrc.Generate},
		{"Proto binary Unicode", "Probe", "中文", "main", "Table", "invalid protobuf", pbdata.Generate},
		{"Go keyword type", "func", "ID", "main", "Table", "invalid go", gosrc.Generate},
		{"Go unexported table", "probe", "ID", "main", "Table", "must be exported", gosrc.Generate},
		{"Go unexported field", "Probe", "id", "main", "Table", "must be exported", gosrc.Generate},
		{"Go package", "Probe", "ID", "a.b", "Table", "single identifier", gosrc.Generate},
		{"C# namespace", "Probe", "ID", "game.class", "Table", "invalid csharp", cssrc.Generate},
		{"Java package", "Probe", "ID", "game.native", "Table", "invalid java", javasrc.Generate},
		{"Proto keyword type", "option", "ID", "main", "Table", "invalid protobuf", pbsrc.Generate},
		{"Proto package", "Probe", "ID", "game-bad", "Table", "invalid protobuf", pbsrc.Generate},
		{"Lua root", "Probe", "ID", "main", "end", "invalid lua", luasrc.Generate},
		{"C# enclosing member", "Probe", "Probe", "main", "Table", "enclosing type", cssrc.Generate},
		{"Java helper type", "TableEvent", "ID", "main", "Table", "conflicts", javasrc.Generate},
		{"root struct", "Table", "ID", "main", "Table", "conflicts", gosrc.Generate},
		{"Go init type", "init", "ID", "main", "Table", "conflicts", gosrc.Generate},
		{"C# reader namespace", "tabtoy", "ID", "main", "Table", "conflicts", cssrc.Generate},
		{"Java boxed type", "Integer", "ID", "main", "Table", "conflicts", javasrc.Generate},
		{"Go helper type", "TableEnumValue", "ID", "main", "Table", "conflicts", gosrc.Generate},
		{"Go method table", "ResetData", "ID", "main", "Table", "conflicts", gosrc.Generate},
		{"Go blank", "Probe", "_", "main", "Table", "invalid go", gosrc.Generate},
		{"C# dotted namespace", "Probe", "ID", "game.config", "Table", "", cssrc.Generate},
		{"Java dotted package", "Probe", "ID", "game.config", "Table", "", javasrc.Generate},
		{"Proto empty package", "Probe", "ID", "", "Table", "", pbsrc.Generate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			emu := NewTableEmulator(t)
			emu.G.PackageName, emu.G.CombineStructName = tc.pkg, tc.root
			idx := emu.CreateCSVFile("Index")
			helper.WriteIndexTableHeader(idx)
			helper.WriteRowValues(idx, "类型表", "", "Type")
			helper.WriteRowValues(idx, "数据表", tc.object, "Data")
			types := emu.CreateCSVFile("Type")
			helper.WriteTypeTableHeader(types)
			helper.WriteRowValues(types, "表头", tc.object, tc.field, tc.field, "int32")
			data := emu.CreateCSVFile("Data")
			helper.WriteRowValues(data, tc.field)
			helper.WriteRowValues(data, "1")
			if err := compiler.Compile(emu.G); err != nil {
				t.Fatal(err)
			}
			// Plain JSON isn't restricted by unrelated output-language keywords.
			if _, err := jsondata.Generate(emu.G); err != nil {
				t.Fatal(err)
			}
			_, err := tc.generate(emu.G)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q, got %v", tc.want, err)
			}
		})
	}
}

func TestFilteredOutputNames(t *testing.T) {
	for _, tc := range []struct {
		name, field, action string
		generate            gen.GenSingleFile
	}{
		{"C#", "class", model.ActionNoGennFieldCsharp, cssrc.Generate},
		{"Lua", "end", model.ActionNoGennFieldLua, luasrc.Generate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			emu := indexedTable(t, "int32", "", "1")
			types := emu.CreateCSVFile("Type")
			helper.WriteRowValues(types, "种类", "对象类型", "标识名", "字段名", "字段类型", "数组切割", "值", "索引", "标记")
			helper.WriteRowValues(types, "表头", "Probe", "ID", "ID", "int32", "", "", "是")
			helper.WriteRowValues(types, "表头", "Probe", "Marker", "Marker", "string")
			helper.WriteRowValues(types, "表头", "Probe", tc.field, tc.field, "int32", "", "", "", "hide")
			emu.G.TagActions = []model.TagAction{{Verb: tc.action, Tags: []string{"hide"}}}
			if err := compiler.Compile(emu.G); err != nil {
				t.Fatal(err)
			}
			if _, err := tc.generate(emu.G); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGeneratedNameCollisions(t *testing.T) {
	for _, tc := range []struct {
		name     string
		rows     [][]string
		generate gen.GenSingleFile
	}{
		{"Proto enum package scope", [][]string{{"枚举", "State", "", "None", "int32", "", "0"}, {"枚举", "Other", "", "None", "int32", "", "0"}}, pbsrc.Generate},
		{"Proto case-insensitive field names", [][]string{{"表头", "Probe", "ID", "ID", "int32"}, {"表头", "Probe", "id", "id", "int32"}}, pbsrc.Generate},
		{"Proto enum prefix names", [][]string{{"枚举", "State", "", "None", "int32", "", "0"}, {"枚举", "State", "", "STATE_NONE", "int32", "", "1"}}, pbsrc.Generate},
		{"Proto JSON field names", [][]string{{"表头", "Probe", "foo_bar", "foo_bar", "int32"}, {"表头", "Probe", "fooBar", "fooBar", "int32"}}, pbsrc.Generate},
		{"root enum", [][]string{{"枚举", "Table", "", "None", "int32", "", "0"}}, gosrc.Generate},
		{"Go enum helper", [][]string{{"枚举", "State", "", "None", "int32", "", "0"}, {"表头", "StateEnumValues", "ID", "ID", "int32"}}, gosrc.Generate},
		{"Java enum member", [][]string{{"枚举", "State", "", "State", "int32", "", "0"}}, javasrc.Generate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			emu := NewTableEmulator(t)
			idx := emu.CreateCSVFile("Index")
			helper.WriteIndexTableHeader(idx)
			helper.WriteRowValues(idx, "类型表", "", "Type")
			types := emu.CreateCSVFile("Type")
			helper.WriteTypeTableHeader(types)
			for _, row := range tc.rows {
				helper.WriteRowValues(types, row...)
			}
			if err := compiler.Compile(emu.G); err != nil {
				t.Fatal(err)
			}
			if _, err := tc.generate(emu.G); err == nil || !strings.Contains(err.Error(), "conflict") {
				t.Fatalf("name collision wasn't rejected: %v", err)
			}
		})
	}
	for _, generate := range []gen.GenSingleFile{gosrc.Generate, cssrc.Generate, javasrc.Generate, luasrc.Generate} {
		emu := indexedTable(t, "int32", "", "1")
		helper.WriteRowValues(emu.sheet("Index"), "数据表", "ProbeByID", "Other")
		helper.WriteRowValues(emu.sheet("Type"), "表头", "ProbeByID", "Value", "Value", "string")
		other := emu.CreateCSVFile("Other")
		helper.WriteRowValues(other, "Value")
		helper.WriteRowValues(other, "test")
		if err := compiler.Compile(emu.G); err != nil {
			t.Fatal(err)
		}
		if _, err := generate(emu.G); err == nil || !strings.Contains(err.Error(), "conflict") {
			t.Fatalf("generated index collided with table name: %v", err)
		}
	}
}
