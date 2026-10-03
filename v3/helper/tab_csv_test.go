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
