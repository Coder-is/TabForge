package tests

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/Coder-is/TabForge/v3/compiler"
	"github.com/Coder-is/TabForge/v3/gen/javasrc"
	"github.com/Coder-is/TabForge/v3/gen/jsondata"
	"github.com/Coder-is/TabForge/v3/helper"
)

// Java has no unsigned integers: uint32 needs long, and uint64 needs BigInteger
// so that JSON numbers above Long.MAX_VALUE load without overflow.
func TestJavaUnsignedTypes(t *testing.T) {
	emu := NewTableEmulator(t)
	idx := emu.CreateCSVFile("Index")
	helper.WriteIndexTableHeader(idx)
	helper.WriteRowValues(idx, "类型表", "", "Type")
	helper.WriteRowValues(idx, "数据表", "Probe", "Data")
	types := emu.CreateCSVFile("Type")
	helper.WriteTypeTableHeader(types)
	helper.WriteRowValues(types, "表头", "Probe", "", "U32", "uint32", "", "", "是")
	helper.WriteRowValues(types, "表头", "Probe", "", "U64", "uint64", "", "", "是")
	helper.WriteRowValues(types, "表头", "Probe", "", "U", "uint")
	helper.WriteRowValues(types, "表头", "Probe", "", "U16", "uint16")
	helper.WriteRowValues(types, "表头", "Probe", "", "U32List", "uint32", "|")
	helper.WriteRowValues(types, "表头", "Probe", "", "U64List", "uint64", "|")
	data := emu.CreateCSVFile("Data")
	helper.WriteRowValues(data, "U32", "U64", "U", "U16", "U32List", "U64List")
	helper.WriteRowValues(data, "4294967295", "18446744073709551615", "4294967295", "65535", "4294967295|0", "18446744073709551615|9223372036854775808")
	helper.WriteRowValues(data, "1", "2", "", "", "", "")
	if err := compiler.Compile(emu.G); err != nil {
		t.Fatal(err)
	}

	java, err := javasrc.Generate(emu.G)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"public long U32 = 0;",
		"public java.math.BigInteger U64 = new java.math.BigInteger(\"0\");",
		"public long U = 0;",
		"public int U16 = 0;",
		"public long[] U32List = new long[]{};",
		"public java.math.BigInteger[] U64List = new java.math.BigInteger[]{};",
		"public Map<Long, Probe> ProbeByU32 = new HashMap<Long, Probe>();",
		"public Map<java.math.BigInteger, Probe> ProbeByU64 = new HashMap<java.math.BigInteger, Probe>();",
	} {
		if !strings.Contains(string(java), want) {
			t.Errorf("generated Java lacks %q", want)
		}
	}
	if t.Failed() {
		t.Fatalf("generated Java:\n%s", java)
	}

	// The Java example loads this JSON directly into the fields above, so the
	// values must stay exact JSON integers rather than wrapped or rounded ones.
	out, err := jsondata.Generate(emu.G)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(out)))
	decoder.UseNumber()
	var tables struct {
		Probe []map[string]interface{}
	}
	if err := decoder.Decode(&tables); err != nil {
		t.Fatal(err)
	}
	row := tables.Probe[0]
	text := func(v interface{}) string { return fmt.Sprint(v) }
	if text(row["U32"]) != "4294967295" || text(row["U"]) != "4294967295" || text(row["U64"]) != "18446744073709551615" ||
		text(row["U32List"]) != "[4294967295 0]" || text(row["U64List"]) != "[18446744073709551615 9223372036854775808]" {
		t.Fatalf("unsigned maximums changed in JSON: %s", out)
	}
	// Values above Long.MAX_VALUE are what a signed long mapping would break.
	if v, _ := new(big.Int).SetString(text(row["U64"]), 10); v.IsInt64() {
		t.Fatalf("uint64 maximum must exceed the signed range: %v", v)
	}
}

func layoutTables(t *testing.T, create func(*TableEmulator, string) helper.TableSheet, header []string, rows ...[]string) *TableEmulator {
	t.Helper()
	emu := NewTableEmulator(t)
	idx := create(emu, "Index")
	helper.WriteIndexTableHeader(idx)
	helper.WriteRowValues(idx, "类型表", "", "Type")
	helper.WriteRowValues(idx, "数据表", "Probe", "Data")
	types := create(emu, "Type")
	helper.WriteTypeTableHeader(types)
	helper.WriteRowValues(types, "表头", "Probe", "编号", "ID", "int32")
	helper.WriteRowValues(types, "表头", "Probe", "名称", "Name", "string")
	data := create(emu, "Data")
	helper.WriteRowValues(data, header...)
	for _, row := range rows {
		helper.WriteRowValues(data, row...)
	}
	return emu
}

var sheetCreators = map[string]func(*TableEmulator, string) helper.TableSheet{
	"csv":  func(emu *TableEmulator, name string) helper.TableSheet { return emu.CreateCSVFile(name) },
	"xlsx": func(emu *TableEmulator, name string) helper.TableSheet { return emu.CreateXLSXFile(name) },
}

const probeJSON = `{ "@Tool": "github.com/Coder-is/TabForge", "@Version": "testver", "Probe": [ %s ] }`

// Fully empty rows used to end the table silently; now they are skipped.
func TestEmptyRowsAreSkipped(t *testing.T) {
	for format, create := range sheetCreators {
		t.Run(format, func(t *testing.T) {
			emu := layoutTables(t, create, []string{"ID", "Name"},
				[]string{"1", "a"}, []string{"", ""}, []string{}, []string{"#2", "comment"}, []string{"3", "c"}, []string{"", ""})
			emu.VerifyData(fmt.Sprintf(probeJSON, `{ "ID": 1, "Name": "a" }, { "ID": 3, "Name": "c" }`))
		})
	}
}

// An empty header no longer ends the header row: later columns are still read.
func TestColumnsAfterEmptyHeader(t *testing.T) {
	for format, create := range sheetCreators {
		t.Run(format, func(t *testing.T) {
			emu := layoutTables(t, create, []string{"ID", "", "#备注", "Name", "", ""},
				[]string{"1", "", "note", "a", "", ""}, []string{"#2", "comment under empty header", "", "b"})
			emu.VerifyData(fmt.Sprintf(probeJSON, `{ "ID": 1, "Name": "a" }`))
		})
	}
}

// Data under an empty header is reported with its location instead of being ignored.
func TestDataUnderEmptyHeader(t *testing.T) {
	for format, create := range sheetCreators {
		t.Run(format, func(t *testing.T) {
			// Between headers.
			emu := layoutTables(t, create, []string{"ID", "", "Name"}, []string{"1", "", "a"}, []string{"2", "lost", "b"})
			emu.MustGotError("TableError.DataUnderEmptyHeader 列头为空的列中有数据, 备注列请使用#开头的列头 | 'lost' @Data|" + sheetName(format) + "(B3)")

			// To the right of the last header, after an empty row.
			emu = layoutTables(t, create, []string{"ID", "Name"}, []string{"1", "a"}, []string{"", ""}, []string{"", "", "", "note"})
			emu.MustGotError("TableError.DataUnderEmptyHeader 列头为空的列中有数据, 备注列请使用#开头的列头 | 'note' @Data|" + sheetName(format) + "(D4)")

			// A sheet without any header still must not hide data.
			emu = layoutTables(t, create, []string{""}, []string{"", "orphan"})
			emu.MustGotError("TableError.DataUnderEmptyHeader 列头为空的列中有数据, 备注列请使用#开头的列头 | 'orphan' @Data|" + sheetName(format) + "(B2)")
		})
	}
}

func sheetName(format string) string {
	if format == "xlsx" {
		return "Default"
	}
	return ""
}

// Workbooks often carry formatted but empty rows and columns far beyond the data.
func TestTrailingFormattedCellsInXlsx(t *testing.T) {
	emu := layoutTables(t, sheetCreators["xlsx"], []string{"ID", "Name"}, []string{"1", "a"})
	data := emu.sheet("Data").(*helper.XlsxSheet)
	blank := make([]string, 50)
	for i := 0; i < 5000; i++ {
		helper.WriteRowValues(data, blank...)
	}
	// A cell styled in the last row/column only extends the sheet dimension.
	data.Sheet.Cell(5000, 300).SetStyle(data.Sheet.Cell(0, 0).GetStyle())
	begin := time.Now()
	emu.VerifyData(fmt.Sprintf(probeJSON, `{ "ID": 1, "Name": "a" }`))
	if elapsed := time.Since(begin); elapsed > 10*time.Second {
		t.Fatalf("trailing empty cells took %v", elapsed)
	}
}

// Index, type and KV tables accept the English field names as header aliases.
func TestEnglishBuiltinHeaders(t *testing.T) {
	build := func(create func(*TableEmulator, string) helper.TableSheet, english bool) *TableEmulator {
		pick := func(cn, en []string) []string {
			if english {
				return en
			}
			return cn
		}
		emu := NewTableEmulator(t)
		idx := create(emu, "Index")
		helper.WriteRowValues(idx, pick([]string{"模式", "表类型", "表文件名", "标记"}, []string{"Kind", "TableType", "TableFileName", "Tags"})...)
		helper.WriteRowValues(idx, "类型表", "", "Type", "")
		helper.WriteRowValues(idx, "Data", "Probe", "Data", "a|b")
		helper.WriteRowValues(idx, "KeyValue", "Config", "KV", "")
		types := create(emu, "Type")
		helper.WriteRowValues(types, pick(
			[]string{"种类", "对象类型", "标识名", "字段名", "字段类型", "数组切割", "值", "索引", "标记"},
			[]string{"Kind", "ObjectType", "Name", "FieldName", "FieldType", "ArraySplitter", "Value", "MakeIndex", "Tags"})...)
		helper.WriteRowValues(types, "枚举", "Color", "", "None", "int", "", "0")
		helper.WriteRowValues(types, "Enum", "Color", "红", "Red", "int", "", "1")
		helper.WriteRowValues(types, "表头", "Probe", "编号", "ID", "int32", "", "", "是", "x")
		helper.WriteRowValues(types, "HeaderStruct", "Probe", "颜色", "Colors", "Color", "|")
		data := create(emu, "Data")
		helper.WriteRowValues(data, "编号", "Colors")
		helper.WriteRowValues(data, "7", "红|Red")
		kv := create(emu, "KV")
		helper.WriteRowValues(kv, pick([]string{"字段名", "字段类型", "标识名", "值", "数组切割", "标记"}, []string{"FieldName", "FieldType", "Name", "Value", "ArraySplitter", "Tags"})...)
		helper.WriteRowValues(kv, "Ports", "uint16", "端口", "80;443", ";", "net")
		return emu
	}
	for format, create := range sheetCreators {
		t.Run(format, func(t *testing.T) {
			var outputs [2]string
			for i, english := range []bool{false, true} {
				emu := build(create, english)
				if err := compiler.Compile(emu.G); err != nil {
					t.Fatalf("english=%v: %v", english, err)
				}
				out, err := jsondata.Generate(emu.G)
				if err != nil {
					t.Fatal(err)
				}
				outputs[i] = string(out) + string(emu.G.Types.ToJSON()) + fmt.Sprint(emu.G.IndexList[1].Tags)
			}
			if outputs[0] != outputs[1] {
				t.Fatalf("English headers changed output:\n%s\n----\n%s", outputs[0], outputs[1])
			}
			for _, want := range []string{`"Colors": [`, `"Ports": [`, `"MakeIndex": true`, "[a b]"} {
				if !strings.Contains(outputs[1], want) {
					t.Fatalf("output lacks %s:\n%s", want, outputs[1])
				}
			}
		})
	}

	// Both spellings of one field in the same header are duplicates.
	emu := NewTableEmulator(t)
	idx := emu.CreateCSVFile("Index")
	helper.WriteRowValues(idx, "模式", "Kind", "表类型", "表文件名")
	helper.WriteRowValues(idx, "类型表", "类型表", "", "Type")
	emu.MustGotError("TableError.DuplicateHeaderField 表头字段重复 | '模式' @Index|(A1)")
}
