package jsondata

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/Coder-is/TabForge/v3/compiler"
	"github.com/Coder-is/TabForge/v3/helper"
	"github.com/Coder-is/TabForge/v3/model"
)

// referenceDocument is the map-based exporter the streaming writer
// replaced; the two must produce the same bytes and the same errors.
func referenceDocument(globals *model.Globals, tables []*model.DataTable, action string) ([]byte, error) {
	doc := map[string]interface{}{"@Tool": "github.com/Coder-is/TabForge", "@Version": globals.Version}
	for _, tab := range tables {
		headers := globals.Types.AllFieldByName(tab.OriginalHeaderType)
		var rows []map[string]interface{}
		for row := 1; row < len(tab.Rows); row++ {
			values := make(map[string]interface{})
			for col, field := range headers {
				if globals.CanDoAction(action, field) {
					continue
				}
				values[field.FieldName] = wrapValue(globals, tab.GetCell(row, col), field)
			}
			rows = append(rows, values)
		}
		doc[tab.HeaderType] = rows
	}
	return json.MarshalIndent(doc, "", "\t")
}

func edgeCaseTables(t *testing.T, floatValue string) *model.Globals {
	mem := helper.NewMemFile()
	idx := mem.CreateCSVFile("Index")
	helper.WriteIndexTableHeader(idx)
	helper.WriteRowValues(idx, "类型表", "", "Type")
	helper.WriteRowValues(idx, "数据表", "", "Data")
	helper.WriteRowValues(idx, "数据表", "", "Empty")
	helper.WriteRowValues(idx, "键值表", "Config", "KV")
	types := mem.CreateCSVFile("Type")
	helper.WriteTypeTableHeader(types)
	helper.WriteRowValues(types, "枚举", "Color", "红", "Red", "int", "", "0", "")
	helper.WriteRowValues(types, "枚举", "Color", "绿", "Green", "int", "", "1", "")
	for _, f := range [][2]string{
		{"S", "string"}, {"F", "float"}, {"D", "float64"}, {"I16", "int16"}, {"I64", "int64"},
		{"U16", "uint16"}, {"U64", "uint64"}, {"B", "bool"}, {"C", "Color"}, {"Hidden", "int32"},
	} {
		helper.WriteRowValues(types, "表头", "Data", f[0], f[0], f[1], "", "", "")
	}
	helper.WriteRowValues(types, "表头", "Data", "Arr", "Arr", "int32", "|", "", "")
	helper.WriteRowValues(types, "表头", "Data", "Names", "Names", "string", ";", "", "")
	helper.WriteRowValues(types, "表头", "Empty", "Only", "Only", "int32", "", "", "")
	data := mem.CreateCSVFile("Data")
	helper.WriteRowValues(data, "S", "F", "D", "I16", "I64", "U16", "U64", "B", "C", "Hidden", "Arr", "Names")
	helper.WriteRowValues(data, `<a href="x">&</a>`, floatValue, "1e-7", "-32768", "-9223372036854775808", "65535", "18446744073709551615", "true", "绿", "1", "1|2|3", "a;<b>;")
	helper.WriteRowValues(data, "tab\there\nline   emoji😀 中文 \"q\" \\", "", "", "", "", "", "", "", "", "", "", "")
	helper.WriteRowValues(data, "", "0.1", "3.141592653589793", "7", "7", "7", "0", "false", "红", "", "5", "")
	empty := mem.CreateCSVFile("Empty")
	helper.WriteRowValues(empty, "Only")
	kv := mem.CreateCSVFile("KV")
	helper.WriteRowValues(kv, "字段名", "字段类型", "标识名", "值")
	helper.WriteRowValues(kv, "Name", "string", "", "x&y")
	helper.WriteRowValues(kv, "Rate", "float", "", "2.5")
	g := model.NewGlobals()
	g.Version, g.IndexFile, g.PackageName, g.CombineStructName = "v<1>", "Index", "main", "Table"
	g.TableGetter, g.IndexGetter = mem, mem
	if err := compiler.Compile(g); err != nil {
		t.Fatal(err)
	}
	g.Types.FieldByName("Data", "Hidden").Tags = []string{"hide"}
	return g
}

func TestStreamMatchesMarshalIndent(t *testing.T) {
	for _, actions := range [][]model.TagAction{nil, {{Verb: model.ActionNoGenFieldJson, Tags: []string{"hide"}}}} {
		g := edgeCaseTables(t, "3.4028235e38")
		g.TagActions = actions
		tables := g.Datas.AllTables()
		cases := map[string][]*model.DataTable{"all": tables}
		for _, tab := range tables {
			cases[tab.HeaderType] = []*model.DataTable{tab}
		}
		for name, set := range cases {
			want, wantErr := referenceDocument(g, set, model.ActionNoGenFieldJson)
			got, err := document(g, set, model.ActionNoGenFieldJson)
			if (err == nil) != (wantErr == nil) || !bytes.Equal(got, want) {
				t.Fatalf("%s (actions %v):\n got %v\n%s\nwant %v\n%s", name, actions, err, got, wantErr, want)
			}
		}
	}
	// Values encoding/json refuses still fail the export.
	g := edgeCaseTables(t, "NaN")
	if _, err := referenceDocument(g, g.Datas.AllTables(), ""); err == nil {
		t.Skip("NaN is not accepted by the checker; nothing to compare")
	}
	if _, err := document(g, g.Datas.AllTables(), ""); err == nil {
		t.Fatal("NaN exported")
	}
}
