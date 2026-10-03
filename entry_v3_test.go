package main

import (
	"bytes"
	"errors"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Coder-is/TabForge/v3/compiler"
	"github.com/Coder-is/TabForge/v3/helper"
	"github.com/Coder-is/TabForge/v3/model"
)

func TestGenFilesWaitsForAllExports(t *testing.T) {
	dir, err := ioutil.TempDir("", "tabtoy-export-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	failedName, successName := "failed-output", filepath.Join(dir, "result.json")
	failed, started, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- genFiles(model.NewGlobals(), []V3GenEntry{
			{name: "failed", param: &failedName, genSingleFile: func(*model.Globals) ([]byte, error) {
				close(failed)
				return nil, errors.New("cannot generate")
			}},
			{name: "success", param: &successName, genSingleFile: func(*model.Globals) ([]byte, error) {
				close(started)
				<-release
				return []byte(`{"ok":true}`), nil
			}},
		})
	}()
	<-failed
	<-started
	var result error
	returned := false
	select {
	case result = <-done:
		returned = true
		t.Error("returned before the other exporter finished")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if !returned {
		select {
		case result = <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("export tasks did not finish")
		}
	}
	if result == nil || !strings.Contains(result.Error(), "failed (failed-output): cannot generate") {
		t.Fatalf("missing error context: %v", result)
	}
	data, err := ioutil.ReadFile(successName)
	if err != nil || string(data) != `{"ok":true}` {
		t.Fatalf("other output was not completed: %s, %v", data, err)
	}
}

func TestV3ExampleExportConsistency(t *testing.T) {
	dir, err := ioutil.TempDir("", "tabtoy-example-export-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// Index table paths in the examples are relative to the example directory.
	if err := os.Chdir(filepath.Join(cwd, "v3", "example", "xlsx")); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(cwd)
	baseline := make(map[string][]byte)
	for _, mode := range []string{"uncached", "cold", "warm", "parallel"} {
		t.Run(mode, func(t *testing.T) {
			globals := model.NewGlobals()
			globals.IndexFile = "Index.xlsx"
			globals.PackageName = "main"
			globals.CombineStructName = "Table"
			globals.GenBinary = true
			globals.ParaLoading = mode == "parallel"
			if mode != "uncached" {
				globals.CacheDir = filepath.Join(dir, "cache")
			}
			globals.IndexGetter = helper.NewFileLoader(true, globals.CacheDir)
			if err := compiler.Compile(globals); err != nil {
				t.Fatal(err)
			}
			var entries []V3GenEntry
			for _, entry := range v3GenList {
				if entry.name == "pbjson" {
					continue // ProtoJSON requires an existing descriptor, tested separately.
				}
				output := filepath.Join(dir, mode, entry.name)
				if entry.genCustom != nil {
					if err := os.MkdirAll(output, 0755); err != nil {
						t.Fatal(err)
					}
				}
				entry.param = &output
				entries = append(entries, entry)
			}
			if err := genFiles(globals, entries); err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(dir, mode)
			err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if info.IsDir() {
					return nil
				}
				name, err := filepath.Rel(root, path)
				if err != nil {
					return err
				}
				data, err := ioutil.ReadFile(path)
				if err != nil {
					return err
				}
				if mode == "uncached" {
					baseline[name] = data
				} else if expected, ok := baseline[name]; !ok || !bytes.Equal(expected, data) {
					t.Errorf("%s differs from uncached output", name)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			for name := range baseline {
				if _, err := os.Stat(filepath.Join(root, name)); err != nil {
					t.Errorf("missing output %s: %v", name, err)
				}
			}
		})
	}
}

func TestGenFilesReportsAllFailures(t *testing.T) {
	first, second, disabled := "first", "second", ""
	err := genFiles(model.NewGlobals(), []V3GenEntry{
		{name: "one", param: &first, genSingleFile: func(*model.Globals) ([]byte, error) {
			return nil, errors.New("first failure")
		}},
		{name: "two", param: &second, genCustom: func(*model.Globals, string) error {
			return errors.New("second failure")
		}},
		{name: "disabled", param: &disabled, genSingleFile: func(*model.Globals) ([]byte, error) {
			panic("disabled exporter was called")
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "one (first): first failure; two (second): second failure") {
		t.Fatalf("expected both export errors: %v", err)
	}
}

func TestValidateProtoOptions(t *testing.T) {
	oldDesc, oldMap, oldProto, oldJSON := *paramProtoDescriptor, *paramProtoMapping, *paramProtoOut, *paramProtoJSONOut
	defer func() {
		*paramProtoDescriptor, *paramProtoMapping, *paramProtoOut, *paramProtoJSONOut = oldDesc, oldMap, oldProto, oldJSON
	}()
	for _, tc := range []struct {
		desc, mapping, generated, json string
		valid                          bool
	}{
		{"", "", "", "", true},
		{"schema.pb", "mapping.json", "", "data.json", true},
		{"schema.pb", "", "", "", false},
		{"", "mapping.json", "", "", false},
		{"schema.pb", "mapping.json", "schema.proto", "", false},
		{"", "", "", "data.json", false},
	} {
		*paramProtoDescriptor, *paramProtoMapping, *paramProtoOut, *paramProtoJSONOut = tc.desc, tc.mapping, tc.generated, tc.json
		if err := validateProtoOptions(); (err == nil) != tc.valid {
			t.Errorf("options %+v: %v", tc, err)
		}
	}
}
