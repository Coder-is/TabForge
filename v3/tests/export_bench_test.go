package tests

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/Coder-is/TabForge/v3/compiler"
	"github.com/Coder-is/TabForge/v3/gen/bindata"
	"github.com/Coder-is/TabForge/v3/gen/jsondata"
	"github.com/Coder-is/TabForge/v3/gen/luasrc"
	"github.com/Coder-is/TabForge/v3/helper"
	"github.com/Coder-is/TabForge/v3/model"
)

// largeTable is a 20000-row table with 40 columns: scalars of each kind
// and a repeated array column. Run with
//
//	go test -run '^$' -bench Large -benchmem ./v3/tests/
func largeTable(rows int) *helper.MemFile {
	mem := helper.NewMemFile()
	idx := mem.CreateCSVFile("Index")
	helper.WriteIndexTableHeader(idx)
	helper.WriteRowValues(idx, "类型表", "", "Type")
	helper.WriteRowValues(idx, "数据表", "", "Big")
	types := mem.CreateCSVFile("Type")
	helper.WriteTypeTableHeader(types)
	kinds := []string{"int32", "string", "float", "bool", "int64", "uint32"}
	var header []string
	for i := 0; i < 36; i++ {
		name := fmt.Sprintf("F%d", i)
		index := ""
		if i == 0 {
			index = "true"
		}
		helper.WriteRowValues(types, "表头", "Big", name, name, kinds[i%len(kinds)], "", "", index)
		header = append(header, name)
	}
	helper.WriteRowValues(types, "表头", "Big", "Tags", "Tags", "int32", "|", "", "")
	header = append(header, "Tags", "Tags", "Tags", "Tags")
	data := mem.CreateCSVFile("Big")
	helper.WriteRowValues(data, header...)
	for r := 0; r < rows; r++ {
		row := make([]string, 0, len(header))
		for i := 0; i < 36; i++ {
			switch kinds[i%len(kinds)] {
			case "string":
				row = append(row, "名字"+strconv.Itoa(r*36+i))
			case "float":
				row = append(row, strconv.FormatFloat(float64(r)+0.25, 'f', -1, 64))
			case "bool":
				row = append(row, strconv.FormatBool(r%2 == 0))
			default:
				if i == 0 {
					row = append(row, strconv.Itoa(r+1))
				} else {
					row = append(row, strconv.Itoa(r*7+i))
				}
			}
		}
		row = append(row, "1", "2", "", "3")
		helper.WriteRowValues(data, row...)
	}
	return mem
}

func compileLarge(b *testing.B, mem *helper.MemFile) *model.Globals {
	g := model.NewGlobals()
	g.Version, g.IndexFile, g.PackageName, g.CombineStructName = "bench", "Index", "main", "Table"
	g.TableGetter, g.IndexGetter = mem, mem
	g.GenBinary = true
	if err := compiler.Compile(g); err != nil {
		b.Fatal(err)
	}
	return g
}

func BenchmarkLargeCompile(b *testing.B) {
	mem := largeTable(20000)
	b.ReportAllocs()
	for b.Loop() {
		compileLarge(b, mem)
	}
}

func BenchmarkLargeExport(b *testing.B) {
	g := compileLarge(b, largeTable(20000))
	for name, generate := range map[string]func(*model.Globals) ([]byte, error){
		"json": jsondata.Generate, "lua": luasrc.Generate, "binary": bindata.Generate,
	} {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := generate(g); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
