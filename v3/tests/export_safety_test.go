package tests

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Coder-is/TabForge/v3/compiler"
	"github.com/Coder-is/TabForge/v3/gen/cssrc"
	"github.com/Coder-is/TabForge/v3/gen/gosrc"
	"github.com/Coder-is/TabForge/v3/gen/javasrc"
	"github.com/Coder-is/TabForge/v3/gen/luasrc"
	"github.com/Coder-is/TabForge/v3/helper"
	"github.com/Coder-is/TabForge/v3/model"
)

// No exporter supports nested structs, so table struct types must not be field types.
func TestStructFieldTypeRejected(t *testing.T) {
	// Used by a data table header.
	emu := NewTableEmulator(t)
	idx := emu.CreateCSVFile("Index")
	helper.WriteIndexTableHeader(idx)
	helper.WriteRowValues(idx, "类型表", "", "Type")
	helper.WriteRowValues(idx, "数据表", "B", "Data")
	types := emu.CreateCSVFile("Type")
	helper.WriteTypeTableHeader(types)
	helper.WriteRowValues(types, "表头", "A", "ID", "ID", "int")
	helper.WriteRowValues(types, "表头", "B", "Ref", "Ref", "A")
	data := emu.CreateCSVFile("Data")
	helper.WriteRowValues(data, "Ref")
	helper.WriteRowValues(data, "1")
	emu.MustGotError("TableError.UnknownFieldType 未知字段类型 | A 'Ref' @Data|(A1)")

	// Defined in the type table without a data table.
	emu = NewTableEmulator(t)
	idx = emu.CreateCSVFile("Index")
	helper.WriteIndexTableHeader(idx)
	helper.WriteRowValues(idx, "类型表", "", "Type")
	types = emu.CreateCSVFile("Type")
	helper.WriteTypeTableHeader(types)
	helper.WriteRowValues(types, "表头", "A", "ID", "ID", "int")
	helper.WriteRowValues(types, "表头", "B", "Ref", "Ref", "A")
	emu.MustGotError("TableError.UnknownFieldType 未知字段类型 | A 'A' @Type|(E3)")

	// Key-value tables.
	emu = NewTableEmulator(t)
	idx = emu.CreateCSVFile("Index")
	helper.WriteIndexTableHeader(idx)
	helper.WriteRowValues(idx, "类型表", "", "Type")
	helper.WriteRowValues(idx, "键值表", "Config", "KV")
	types = emu.CreateCSVFile("Type")
	helper.WriteTypeTableHeader(types)
	helper.WriteRowValues(types, "表头", "A", "ID", "ID", "int")
	kv := emu.CreateCSVFile("KV")
	helper.WriteRowValues(kv, "字段名", "字段类型", "标识名", "值")
	helper.WriteRowValues(kv, "Ref", "A", "", "1")
	emu.MustGotError("TableError.UnknownFieldType 未知字段类型 | A 'A' @KV|(B2)")
}

// Rows with content only in # comment columns, or only whitespace, are not records.
func TestCommentOnlyRowsSkipped(t *testing.T) {
	for format, create := range sheetCreators {
		t.Run(format, func(t *testing.T) {
			emu := layoutTables(t, create, []string{"ID", "#备注", "Name"},
				[]string{"1", "x", "a"}, []string{"", "only note", ""}, []string{" ", "", "  "}, []string{"2", "", "b"})
			emu.VerifyData(fmt.Sprintf(probeJSON, `{ "ID": 1, "Name": "a" }, { "ID": 2, "Name": "b" }`))

			// Data under an empty header is still reported in such rows.
			emu = layoutTables(t, create, []string{"ID", "", "#备注", "Name"}, []string{"1", "", "", "a"}, []string{"", "lost", "note", ""})
			emu.MustGotError("TableError.DataUnderEmptyHeader 列头为空的列中有数据, 备注列请使用#开头的列头 | 'lost' @Data|" + sheetName(format) + "(B3)")
		})
	}
}

// An empty enum cell resolves to the first member, not to a member without 标识名.
func TestEmptyEnumUsesFirstMember(t *testing.T) {
	emu := NewTableEmulator(t)
	idx := emu.CreateCSVFile("Index")
	helper.WriteIndexTableHeader(idx)
	helper.WriteRowValues(idx, "类型表", "", "Type")
	helper.WriteRowValues(idx, "数据表", "", "Probe")
	types := emu.CreateCSVFile("Type")
	helper.WriteTypeTableHeader(types)
	helper.WriteRowValues(types, "枚举", "ActorType", "无", "None", "int", "", "0")
	helper.WriteRowValues(types, "枚举", "ActorType", "", "Hero", "int", "", "5")
	helper.WriteRowValues(types, "表头", "Probe", "ID", "ID", "int")
	helper.WriteRowValues(types, "表头", "Probe", "类型", "Type", "ActorType")
	data := emu.CreateCSVFile("Probe")
	helper.WriteRowValues(data, "ID", "类型")
	helper.WriteRowValues(data, "1", "")
	emu.VerifyData(fmt.Sprintf(probeJSON, `{ "ID": 1, "Type": 0 }`))

	lua, err := luasrc.Generate(emu.G)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(lua), "Type = 0") {
		t.Fatalf("Lua empty enum:\n%s", lua)
	}
	cs, err := cssrc.Generate(emu.G)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cs), "public ActorType Type = ActorType.None;") {
		t.Fatalf("C# enum default:\n%s", cs)
	}
}

// 标识名 is written into comments and Go tags; line breaks would turn the rest into code.
func TestFieldAliasRejected(t *testing.T) {
	for _, name := range []string{"备注\nos.exit()", "a\rb", "tab\tname", "line\u2028sep", `quo"te`, "back`tick"} {
		emu := NewTableEmulator(t)
		idx := emu.CreateCSVFile("Index")
		helper.WriteIndexTableHeader(idx)
		helper.WriteRowValues(idx, "类型表", "", "Type")
		types := emu.CreateCSVFile("Type")
		helper.WriteTypeTableHeader(types)
		helper.WriteRowValues(types, "枚举", "ActorType", name, "None", "int", "", "0")
		requireTableError(t, compiler.Compile(emu.G), "InvalidFieldAlias")
	}

	emu := NewTableEmulator(t)
	idx := emu.CreateCSVFile("Index")
	helper.WriteIndexTableHeader(idx)
	helper.WriteRowValues(idx, "键值表", "Config", "KV")
	kv := emu.CreateCSVFile("KV")
	helper.WriteRowValues(kv, "字段名", "字段类型", "标识名", "值")
	helper.WriteRowValues(kv, "Name", "string", "名称\nend os.exit() --", "a")
	emu.MustGotError("TableError.InvalidFieldAlias 非法标识名, 不能包含换行等控制字符、双引号或反引号 | '名称\nend os.exit() --' @KV|(C2)")

	// Accepted names are still escaped inside the Go struct tag.
	tag := gosrc.UsefulFunc["GoTabTag"].(func(*model.TypeDefine) string)(&model.TypeDefine{Name: `路径\名称`})
	if tag != "`tb_name:\"路径\\\\名称\"`" {
		t.Fatalf("unsafe Go tag: %s", tag)
	}
}

// Table and object type names are joined into output paths, so they must be identifiers.
func TestTypeNamePathTraversal(t *testing.T) {
	emu := NewTableEmulator(t)
	idx := emu.CreateCSVFile("Index")
	helper.WriteIndexTableHeader(idx)
	helper.WriteRowValues(idx, "类型表", "", "Type")
	helper.WriteRowValues(idx, "数据表", "../evil", "A")
	types := emu.CreateCSVFile("Type")
	helper.WriteTypeTableHeader(types)
	helper.WriteRowValues(types, "表头", "../evil", "ID", "ID", "int")
	emu.MustGotError("TableError.InvalidTypeName 非法类型名, 只能使用标识符(字母、数字、下划线) | ../evil '../evil' @Index|(B3)")

	// Table type derived from the file name.
	emu = NewTableEmulator(t)
	idx = emu.CreateCSVFile("Index")
	helper.WriteIndexTableHeader(idx)
	helper.WriteRowValues(idx, "数据表", "", "dir/my data.csv")
	emu.MustGotError("TableError.InvalidTypeName 非法类型名, 只能使用标识符(字母、数字、下划线) | my data 'dir/my data.csv' @Index|(C2)")

	// Type table object types.
	emu = NewTableEmulator(t)
	idx = emu.CreateCSVFile("Index")
	helper.WriteIndexTableHeader(idx)
	helper.WriteRowValues(idx, "类型表", "", "Type")
	types = emu.CreateCSVFile("Type")
	helper.WriteTypeTableHeader(types)
	helper.WriteRowValues(types, "枚举", `..\evil`, "", "None", "int", "", "0")
	emu.MustGotError(`TableError.InvalidTypeName 非法类型名, 只能使用标识符(字母、数字、下划线) | ..\evil '..\evil' @Type|(B2)`)
}

// fastjson maps integer enums by ordinal, so generated Java enums deserialize by value.
func TestJavaEnumFastjsonDeserializer(t *testing.T) {
	emu := NewTableEmulator(t)
	idx := emu.CreateCSVFile("Index")
	helper.WriteIndexTableHeader(idx)
	helper.WriteRowValues(idx, "类型表", "", "Type")
	helper.WriteRowValues(idx, "数据表", "", "Probe")
	types := emu.CreateCSVFile("Type")
	helper.WriteTypeTableHeader(types)
	helper.WriteRowValues(types, "枚举", "ActorType", "", "None", "int", "", "0")
	helper.WriteRowValues(types, "枚举", "ActorType", "", "Hero", "int", "", "5")
	helper.WriteRowValues(types, "表头", "Probe", "ID", "ID", "int")
	helper.WriteRowValues(types, "表头", "Probe", "", "Type", "ActorType")
	data := emu.CreateCSVFile("Probe")
	helper.WriteRowValues(data, "ID", "Type")
	helper.WriteRowValues(data, "1", "Hero")
	if err := compiler.Compile(emu.G); err != nil {
		t.Fatal(err)
	}
	java, err := javasrc.Generate(emu.G)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"@com.alibaba.fastjson.annotation.JSONType(deserializer = ActorType.FastjsonDeserializer.class)\n\t public enum ActorType {",
		"public static class FastjsonDeserializer implements com.alibaba.fastjson.parser.deserializer.ObjectDeserializer {",
		"return (T) fromInt(((Number) v).intValue());",
		"case 5:\n\t\t\t\treturn Hero;",
	} {
		if !strings.Contains(string(java), want) {
			t.Fatalf("missing %q in:\n%s", want, java)
		}
	}

	// The deserializer's helper names must not be shadowed by user types.
	for _, name := range []string{"FastjsonDeserializer", "Number", "Object", "com"} {
		emu := NewTableEmulator(t)
		idx := emu.CreateCSVFile("Index")
		helper.WriteIndexTableHeader(idx)
		helper.WriteRowValues(idx, "类型表", "", "Type")
		types := emu.CreateCSVFile("Type")
		helper.WriteTypeTableHeader(types)
		helper.WriteRowValues(types, "枚举", name, "", "None", "int", "", "0")
		if err := compiler.Compile(emu.G); err != nil {
			t.Fatal(err)
		}
		if _, err := javasrc.Generate(emu.G); err == nil || !strings.Contains(err.Error(), "conflicts") {
			t.Fatalf("%s: expected conflict, got %v", name, err)
		}
	}
}
