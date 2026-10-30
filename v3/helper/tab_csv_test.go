package helper

import (
	"encoding/csv"
	"io/ioutil"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCSVSaveRoundTrip(t *testing.T) {
	dir, err := ioutil.TempDir("", "tabtoy-csv-save-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	file := NewCSVFile()
	file.sheet.WriteRow("ID", "Name")
	file.sheet.WriteRow("1", "中文,\"quoted\"\nnext line")
	name := filepath.Join(dir, "data.csv")
	if err := file.Save(name); err != nil {
		t.Fatal(err)
	}
	data, err := ioutil.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	records, err := csv.NewReader(strings.NewReader(string(data))).ReadAll()
	if err != nil || !reflect.DeepEqual(records, file.records) {
		t.Fatalf("saved CSV did not round trip: %v, %v", records, err)
	}
	// Windows cannot remove an open file, so this also checks the save handle closes.
	if err := os.Remove(name); err != nil {
		t.Fatalf("saved file still open: %v", err)
	}
}

func TestCSVLoadUTF8BOMAndLateNonASCII(t *testing.T) {
	dir := t.TempDir()
	bom := filepath.Join(dir, "bom.csv")
	if err := os.WriteFile(bom, []byte("\xEF\xBB\xBFID,Name\n1,x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	file := NewCSVFile()
	if err := file.Load(bom); err != nil {
		t.Fatal(err)
	}
	if file.records[0][0] != "ID" {
		t.Fatalf("BOM leaked into first header: %q", file.records[0][0])
	}

	// UTF-8 text after an ASCII-only first kilobyte must not be decoded as GBK.
	late := filepath.Join(dir, "late.csv")
	if err := os.WriteFile(late, []byte("ID,Name\n"+strings.Repeat("1,abc\n", 300)+"2,中文\n"), 0644); err != nil {
		t.Fatal(err)
	}
	file = NewCSVFile()
	if err := file.Load(late); err != nil {
		t.Fatal(err)
	}
	if got := file.records[len(file.records)-1][1]; got != "中文" {
		t.Fatalf("UTF-8 text was transcoded: %q", got)
	}
}

func TestCSVLoadGBK(t *testing.T) {
	data, err := ConvUTF8ToGBK([]byte("ID,名字\n1,中文\n"))
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(t.TempDir(), "gbk.csv")
	if err := os.WriteFile(name, data, 0644); err != nil {
		t.Fatal(err)
	}
	file := NewCSVFile()
	if err := file.Load(name); err != nil {
		t.Fatal(err)
	}
	if file.records[0][1] != "名字" || file.records[1][1] != "中文" {
		t.Fatalf("GBK not decoded: %q", file.records)
	}
}
