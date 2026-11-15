package tests

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Coder-is/TabForge/protocol"
	"github.com/Coder-is/TabForge/v3/compiler"
	"github.com/Coder-is/TabForge/v3/gen"
	"github.com/Coder-is/TabForge/v3/gen/bindata"
	"github.com/Coder-is/TabForge/v3/gen/cssrc"
	"github.com/Coder-is/TabForge/v3/gen/gosrc"
	"github.com/Coder-is/TabForge/v3/gen/javasrc"
	"github.com/Coder-is/TabForge/v3/gen/jsondata"
	"github.com/Coder-is/TabForge/v3/gen/jsontype"
	"github.com/Coder-is/TabForge/v3/gen/luasrc"
	"github.com/Coder-is/TabForge/v3/gen/pbdata"
	"github.com/Coder-is/TabForge/v3/gen/pbsrc"
	"github.com/Coder-is/TabForge/v3/helper"
	"google.golang.org/protobuf/encoding/protojson"
)

// structTables is the example of doc/nested-struct-design.md, with a struct
// inside a struct and an array member inside a struct.
func structTables(t *testing.T, header []string, rows ...[]string) *TableEmulator {
	emu := NewTableEmulator(t)
	idx := emu.CreateCSVFile("Index")
	helper.WriteIndexTableHeader(idx)
	helper.WriteRowValues(idx, "类型表", "", "Type")
	helper.WriteRowValues(idx, "数据表", "Item", "Data")
	types := emu.CreateCSVFile("Type")
	helper.WriteTypeTableHeader(types)
	helper.WriteRowValues(types, "枚举", "Rarity", "普通", "Common", "int32", "", "0")
	helper.WriteRowValues(types, "枚举", "Rarity", "稀有", "Rare", "int32", "", "1")
	helper.WriteRowValues(types, "表头", "Extra", "等级", "Level", "int32")
	helper.WriteRowValues(types, "表头", "Extra", "品质", "Rarity", "Rarity")
	helper.WriteRowValues(types, "表头", "Reward", "物品", "ItemID", "int32")
	helper.WriteRowValues(types, "表头", "Reward", "数量", "Count", "int32")
	helper.WriteRowValues(types, "表头", "Reward", "标签", "Tags", "int32", "|")
	helper.WriteRowValues(types, "表头", "Reward", "附加", "Extra", "Extra")
	helper.WriteRowValues(types, "表头", "Item", "编号", "ID", "int32", "", "", "true")
	helper.WriteRowValues(types, "表头", "Item", "奖励", "Reward", "Reward")
	helper.WriteRowValues(types, "表头", "Item", "消耗", "Costs", "Reward", ";")
	data := emu.CreateCSVFile("Data")
	helper.WriteRowValues(data, header...)
	for _, row := range rows {
		helper.WriteRowValues(data, row...)
	}
	return emu
}

func TestStructFieldsToJSON(t *testing.T) {
	emu := structTables(t,
		[]string{"编号", "奖励.物品", "Reward.Count", "Reward.Tags", "奖励.附加.品质", "消耗[1].物品", "Costs[1].Count", "消耗[2].物品", "Costs[2].Count"},
		[]string{"1", "1001", "5", "1|2", "稀有", "2001", "1", "2002", "3"},
		[]string{"2", "", "", "", "", "2003", "", "", ""},
	)
	if err := compiler.Compile(emu.G); err != nil {
		t.Fatal(err)
	}
	out, err := jsondata.Generate(emu.G)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var want map[string]interface{}
	json.Unmarshal([]byte(`{"@Tool":"github.com/Coder-is/TabForge","@Version":"testver","Item":[
		{"ID":1,"Reward":{"Count":5,"Extra":{"Level":0,"Rarity":1},"ItemID":1001,"Tags":[1,2]},
		 "Costs":[{"Count":1,"Extra":{"Level":0,"Rarity":0},"ItemID":2001,"Tags":[]},{"Count":3,"Extra":{"Level":0,"Rarity":0},"ItemID":2002,"Tags":[]}]},
		{"ID":2,"Reward":{"Count":0,"Extra":{"Level":0,"Rarity":0},"ItemID":0,"Tags":[]},
		 "Costs":[{"Count":0,"Extra":{"Level":0,"Rarity":0},"ItemID":2003,"Tags":[]}]}]}`), &want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got\n%s", out)
	}
	// Keys inside structs are sorted like row keys.
	if !strings.Contains(string(out), "\"Count\": 5,\n\t\t\t\t\"Extra\"") {
		t.Fatalf("struct members not in order:\n%s", out)
	}
}

func TestStructFieldErrors(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		header     []string
		row        []string
	}{
		{"plain header on a struct", "StructFieldNeedsMembers", []string{"编号", "奖励"}, []string{"1", "x"}},
		{"member of a primitive", "HeaderPathInvalid", []string{"编号", "编号.物品"}, []string{"1", "1"}},
		{"element number on a single struct", "HeaderPathInvalid", []string{"编号", "奖励[1].物品"}, []string{"1", "1"}},
		{"struct array without element number", "HeaderPathInvalid", []string{"编号", "消耗.物品"}, []string{"1", "1"}},
		{"element number on a primitive array", "HeaderPathInvalid", []string{"编号", "奖励.标签[1]"}, []string{"1", "1"}},
		{"malformed element number", "HeaderPathInvalid", []string{"编号", "消耗[x].物品"}, []string{"1", "1"}},
		{"unknown member", "HeaderFieldNotDefined", []string{"编号", "奖励.价格"}, []string{"1", "1"}},
		{"struct leaf", "StructFieldNeedsMembers", []string{"编号", "奖励.附加"}, []string{"1", "1"}},
		{"same member twice", "DuplicateHeaderField", []string{"编号", "奖励.数量", "Reward.Count"}, []string{"1", "1", "2"}},
		{"same element member twice", "DuplicateHeaderField", []string{"编号", "消耗[1].数量", "Costs[1].Count"}, []string{"1", "1", "2"}},
		{"bad member value", "DataMissMatchTypeDefine", []string{"编号", "奖励.数量"}, []string{"1", "many"}},
		{"bad member enum", "UnknownEnumValue", []string{"编号", "消耗[3].附加.品质"}, []string{"1", "传说"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := compiler.Compile(structTables(t, tc.header, tc.row).G)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %s, got %v", tc.want, err)
			}
		})
	}
	// Errors in member columns point at the cell in the data sheet.
	err := compiler.Compile(structTables(t, []string{"编号", "奖励.数量"}, []string{"1", "many"}).G)
	if err == nil || !strings.Contains(err.Error(), "@Data|(B2)") {
		t.Fatalf("member error location: %v", err)
	}
}

func TestStructTypeErrors(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		rows       [][]string
	}{
		{"self", "RecursiveStructField", [][]string{{"表头", "Node", "下级", "Next", "Node"}, {"表头", "Node", "值", "V", "int32"}}},
		{"cycle", "RecursiveStructField", [][]string{{"表头", "A", "乙", "B", "B"}, {"表头", "B", "甲", "A", "A"}}},
		{"index on struct", "StructFieldIndex", [][]string{{"表头", "P", "值", "V", "int32"}, {"表头", "Q", "子", "Sub", "P", "", "", "true"}}},
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
			if err := compiler.Compile(emu.G); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %s, got %v", tc.want, err)
			}
		})
	}
}

func compiledStructExample(t *testing.T) *TableEmulator {
	emu := structTables(t,
		[]string{"编号", "奖励.物品", "Reward.Count", "Reward.Tags", "奖励.附加.品质", "消耗[1].物品", "Costs[1].Count", "消耗[2].物品", "Costs[2].Count"},
		[]string{"1", "1001", "5", "1|2", "稀有", "2001", "1", "2002", "3"},
		[]string{"2", "", "", "", "", "2003", "", "", ""},
	)
	if err := compiler.Compile(emu.G); err != nil {
		t.Fatal(err)
	}
	return emu
}

func TestStructFieldsToLua(t *testing.T) {
	out, err := luasrc.Generate(compiledStructExample(t).G)
	if err != nil {
		t.Fatal(err)
	}
	want := "{ ID = 1, Reward = { ItemID = 1001, Count = 5, Tags = {1,2}, Extra = { Level = 0, Rarity = 1, }, }, Costs = {{ ItemID = 2001, Count = 1, Tags = {}, Extra = { Level = 0, Rarity = 0, }, },{ ItemID = 2002, Count = 3, Tags = {}, Extra = { Level = 0, Rarity = 0, }, }}, }"
	if !strings.Contains(string(out), want) {
		t.Fatalf("Lua rows:\n%s", out)
	}
}

// The generated Go types decode the generated JSON.
func TestStructFieldsGoRoundTrip(t *testing.T) {
	emu := compiledStructExample(t)
	src, err := gosrc.Generate(emu.G)
	if err != nil {
		t.Fatal(err)
	}
	data, err := jsondata.Generate(emu.G)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	files := map[string]string{
		"go.mod":     "module structcheck\n\ngo 1.26\n",
		"table.go":   string(src),
		"table.json": string(data),
		"main_run.go": `package main

import (
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	data, _ := os.ReadFile("table.json")
	t := NewTable()
	if err := json.Unmarshal(data, t); err != nil {
		panic(err)
	}
	r := t.Item[0].Reward
	fmt.Println(r.ItemID, r.Count, r.Tags, r.Extra.Rarity, len(t.Item[0].Costs), t.Item[0].Costs[1].Count, len(t.Item[1].Costs), t.Item[1].Costs[0].ItemID)
}
`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("go", "run", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOTOOLCHAIN=local")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "1001 5 [1 2] Rare 2 3 1 2003" {
		t.Fatalf("decoded %q", got)
	}
}

func TestStructFieldsInSources(t *testing.T) {
	g := compiledStructExample(t).G
	for _, tc := range []struct {
		name     string
		generate gen.GenSingleFile
		want     []string
	}{
		{"C#", cssrc.Generate, []string{"public Reward Reward = new Reward();", "public List<Reward> Costs = new List<Reward>();"}},
		{"Java", javasrc.Generate, []string{"public Reward Reward = new Reward();", "public Reward[] Costs = new Reward[]{};"}},
		{"Proto", pbsrc.Generate, []string{"Reward Reward = 2;", "repeated Reward Costs = 3;"}},
	} {
		out, err := tc.generate(g)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		for _, want := range tc.want {
			if !strings.Contains(string(out), want) {
				t.Fatalf("%s lacks %q:\n%s", tc.name, want, out)
			}
		}
		if tc.name == "Proto" {
			dir := t.TempDir()
			os.WriteFile(filepath.Join(dir, "table.proto"), out, 0644)
			if _, err := protocol.CompileSchema(context.Background(), []string{dir}, []string{"table.proto"}); err != nil {
				t.Fatalf("proto: %v\n%s", err, out)
			}
		}
	}
	types, err := jsontype.Generate(g)
	if err != nil || !strings.Contains(string(types), "\"IsStruct\": true") {
		t.Fatalf("jsontype: %v\n%s", err, types)
	}
}

// Tables mapped onto an existing Proto do not take struct fields yet.
func TestStructFieldsRejectedWithProtoMapping(t *testing.T) {
	g := compiledStructExample(t).G
	g.ProtoDescriptorFile, g.ProtoMappingFile = "schema.pb", "mapping.json"
	for name, generate := range map[string]gen.GenSingleFile{"protobuf": pbdata.Generate, "protojson": pbdata.GenerateJSON} {
		if _, err := generate(g); err == nil || !strings.Contains(err.Error(), "StructFieldNotSupported") {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

// The Protobuf export decodes with the generated .proto, structs included.
func TestStructFieldsProtobufRoundTrip(t *testing.T) {
	g := compiledStructExample(t).G
	src, err := pbsrc.Generate(g)
	if err != nil {
		t.Fatal(err)
	}
	data, err := pbdata.Generate(g)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "table.proto"), src, 0644)
	schema, err := protocol.CompileSchema(context.Background(), []string{dir}, []string{"table.proto"})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := schema.DecodeBinary("main.Table", data)
	if err != nil {
		t.Fatal(err)
	}
	out, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	var got, want map[string]interface{}
	json.Unmarshal(out, &got)
	json.Unmarshal([]byte(`{"Item":[
		{"ID":1,"Reward":{"ItemID":1001,"Count":5,"Tags":[1,2],"Extra":{"Rarity":"Rare"}},"Costs":[{"ItemID":2001,"Count":1},{"ItemID":2002,"Count":3}]},
		{"ID":2,"Costs":[{"ItemID":2003}]}]}`), &want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded %s", out)
	}
}

// The generated C# reads the binary export, nested structs included.
// Runs where DOTNET_BIN is set (CI's C# job).
func TestStructFieldsCSharpBinary(t *testing.T) {
	dotnet := os.Getenv("DOTNET_BIN")
	if dotnet == "" {
		t.Skip("DOTNET_BIN is not set")
	}
	emu := compiledStructExample(t)
	emu.G.GenBinary = true
	src, err := cssrc.Generate(emu.G)
	if err != nil {
		t.Fatal(err)
	}
	data, err := bindata.Generate(emu.G)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := os.ReadFile(filepath.Join("..", "api", "csharp", "TableReader.cs"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	files := map[string][]byte{
		"Table.cs":       src,
		"TableReader.cs": reader,
		"table.bin":      data,
		"check.csproj":   []byte(`<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><OutputType>Exe</OutputType><TargetFramework>net8.0</TargetFramework><Nullable>disable</Nullable></PropertyGroup></Project>`),
		"Program.cs": []byte(`using System; using System.IO;
class Program {
	static void Main() {
		using var stream = new FileStream("table.bin", FileMode.Open);
		var tab = new main.Table();
		tab.Deserialize(new tabtoy.TableReader(stream));
		var r = tab.Item[0].Reward;
		Console.WriteLine($"{r.ItemID} {r.Count} {string.Join(",", r.Tags)} {r.Extra.Rarity} {tab.Item[0].Costs.Count} {tab.Item[0].Costs[1].Count} {tab.Item[1].Costs.Count} {tab.Item[1].Costs[0].ItemID} {tab.Item[1].Reward.ItemID}");
	}
}
`),
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(dotnet, "run", "-nologo", "-v", "q")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); !strings.HasSuffix(got, "1001 5 1,2 Rare 2 3 1 2003 0") {
		t.Fatalf("C# read %q", got)
	}
}
