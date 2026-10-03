package util

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/tealeg/xlsx"
)

func writeTestWorkbook(t *testing.T, name, value string) {
	t.Helper()
	f := xlsx.NewFile()
	sheet, err := f.AddSheet("Data")
	if err != nil {
		t.Fatal(err)
	}
	sheet.AddRow().AddCell().Value = value
	if err := f.Save(name); err != nil {
		t.Fatal(err)
	}
}

func TestTableCacheRecovery(t *testing.T) {
	for _, scenario := range []string{"missing data", "corrupt data", "missing hash", "corrupt hash", "mismatched data", "invalid sheets", "changed source"} {
		t.Run(scenario, func(t *testing.T) {
			dir, err := ioutil.TempDir("", "tabtoy-cache-test-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			source := filepath.Join(dir, "source.xlsx")
			writeTestWorkbook(t, source, "original")
			cache := NewTableCache(source, filepath.Join(dir, "nested", "cache"))
			if err := cache.Open(); err != nil {
				t.Fatal(err)
			}
			defer cache.Close()
			if _, err := cache.Load(); err != nil || cache.UseCache() {
				t.Fatalf("cold load: error=%v, cached=%v", err, cache.UseCache())
			}
			if err := cache.Save(); err != nil {
				t.Fatal(err)
			}
			if err := cache.Close(); err != nil {
				t.Fatal(err)
			}
			if err := cache.Open(); err != nil {
				t.Fatal(err)
			}
			if f, err := cache.Load(); err != nil || !cache.UseCache() || f.Sheets[0].Rows[0].Cells[0].Value != "original" {
				t.Fatalf("warm load: file=%v, error=%v, cached=%v", f, err, cache.UseCache())
			}
			if err := cache.Close(); err != nil {
				t.Fatal(err)
			}
			want := "original"
			switch scenario {
			case "missing data":
				err = os.Remove(cache.cacheFileName())
			case "corrupt data":
				err = ioutil.WriteFile(cache.cacheFileName(), []byte("{"), 0600)
			case "missing hash":
				err = os.Remove(cache.hashFileName())
			case "corrupt hash":
				err = ioutil.WriteFile(cache.hashFileName(), []byte("{"), 0600)
			case "mismatched data":
				data := []byte(fmt.Sprintf(`{"Name":%q,"Sheets":[{"Name":"Data","Cells":[["stale"]]}]}`, source))
				err = ioutil.WriteFile(cache.cacheFileName(), data, 0600)
			case "invalid sheets":
				data := []byte(fmt.Sprintf(`{"Name":%q,"Sheets":[null]}`, source))
				var hash XlsxHashFile
				if err := readJsonFile(cache.hashFileName(), &hash); err != nil {
					t.Fatal(err)
				}
				hash.DataHash = fmt.Sprintf("%x", sha256.Sum256(data))
				if err := writeJsonFile(cache.hashFileName(), &hash); err != nil {
					t.Fatal(err)
				}
				err = ioutil.WriteFile(cache.cacheFileName(), data, 0600)
			case "changed source":
				want = "updated"
				writeTestWorkbook(t, source, want)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := cache.Open(); err != nil {
				t.Fatal(err)
			}
			f, err := cache.Load()
			if err != nil {
				t.Fatal(err)
			}
			if cache.UseCache() || len(f.Sheets) != 1 || f.Sheets[0].Rows[0].Cells[0].Value != want {
				t.Fatalf("did not recover source: cached=%v, file=%v", cache.UseCache(), f)
			}
		})
	}
}

func TestTableCacheSaveError(t *testing.T) {
	dir, err := ioutil.TempDir("", "tabtoy-cache-save-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	source := filepath.Join(dir, "source.xlsx")
	writeTestWorkbook(t, source, "value")
	blocked := filepath.Join(dir, "not-a-directory")
	if err := ioutil.WriteFile(blocked, nil, 0600); err != nil {
		t.Fatal(err)
	}
	cache := NewTableCache(source, blocked)
	if err := cache.Open(); err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	if _, err := cache.Load(); err != nil {
		t.Fatal(err)
	}
	if err := cache.Save(); err == nil {
		t.Fatal("cache save must report an unwritable destination")
	}
}

func TestTableCacheClose(t *testing.T) {
	dir, err := ioutil.TempDir("", "tabtoy-cache-close-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	source := filepath.Join(dir, "source.xlsx")
	writeTestWorkbook(t, source, "value")
	cache := NewTableCache(source, dir)
	if err := cache.Open(); err != nil {
		t.Fatal(err)
	}
	reader := cache.z
	if err := cache.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.File[0].Open(); err == nil {
		t.Fatal("ZIP reader is still open")
	}
	if err := cache.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Load(); err == nil {
		t.Fatal("loading a closed cache should fail")
	}
}

func TestCacheWriteFailurePreservesPreviousFile(t *testing.T) {
	dir, err := ioutil.TempDir("", "tabtoy-cache-atomic-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	name := filepath.Join(dir, "cache.json")
	if err := writeJsonFile(name, map[string]string{"value": "original"}); err != nil {
		t.Fatal(err)
	}
	if err := writeJsonFile(name, make(chan int)); err == nil {
		t.Fatal("unsupported JSON should fail")
	}
	data, err := ioutil.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(data, &got); err != nil || got["value"] != "original" {
		t.Fatalf("previous file was damaged: %s, %v", data, err)
	}
	entries, err := ioutil.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("unexpected temporary files: %v, %v", entries, err)
	}
}
