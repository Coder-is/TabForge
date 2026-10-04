package helper

import (
	"testing"

	"github.com/tealeg/xlsx"
)

func TestXlsxFileReplacesSheets(t *testing.T) {
	file := &XlsxFile{}
	for _, name := range []string{"First", "Second"} {
		workbook := xlsx.NewFile()
		if _, err := workbook.AddSheet(name); err != nil {
			t.Fatal(err)
		}
		file.FromXFile(workbook)
		if sheets := file.Sheets(); len(sheets) != 1 || sheets[0].Name() != name {
			t.Fatalf("previous sheets retained after loading %s: %v", name, sheets)
		}
	}
}
