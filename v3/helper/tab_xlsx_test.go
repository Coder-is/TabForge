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

func TestXlsxReadDoesNotGrowSheet(t *testing.T) {
	workbook := xlsx.NewFile()
	if _, err := workbook.AddSheet("Data"); err != nil {
		t.Fatal(err)
	}
	file := &XlsxFile{}
	file.FromXFile(workbook)
	sheet := file.Sheets()[0]
	sheet.WriteRow("ID", "", "Name")
	if sheet.GetValue(1000, 1000, nil) != "" || !sheet.IsRowEmpty(1000, -1) || sheet.IsRowEmpty(0, -1) {
		t.Fatal("unexpected cell values")
	}
	if sheet.MaxRow() != 1 || sheet.MaxColumn() != 3 {
		t.Fatalf("reading grew the sheet to %dx%d", sheet.MaxRow(), sheet.MaxColumn())
	}
}
