package main

import (
	"bytes"
	"encoding/json"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestCLIV3Only(t *testing.T) {
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := ioutil.TempDir("", "tabtoy-cli-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	binary := filepath.Join(dir, "tabtoy")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}

	t.Run("help", func(t *testing.T) {
		output, err := exec.Command(binary, "-h").CombinedOutput()
		if err != nil || !bytes.Contains(output, []byte(`default "v3"`)) {
			t.Fatalf("V3 default missing from help: %v\n%s", err, output)
		}
	})
	for _, mode := range []string{"v2", "exportorv2", "v2tov3"} {
		t.Run("reject-mode-"+mode, func(t *testing.T) {
			output, err := exec.Command(binary, "-mode="+mode).CombinedOutput()
			if err == nil || !bytes.Contains(output, []byte("only v3 is supported")) {
				t.Fatalf("obsolete mode accepted or unclear error: %v\n%s", err, output)
			}
		})
	}
	for _, name := range []string{"protover", "luaenumintvalue", "luatabheader", "cs_gensercode", "up_out", "lan", "modlistfile", "pbt_out", "type_out", "cpp_out"} {
		t.Run("reject-flag-"+name, func(t *testing.T) {
			output, err := exec.Command(binary, "-"+name+"=unused").CombinedOutput()
			if err == nil || !bytes.Contains(output, []byte("flag provided but not defined: -"+name)) {
				t.Fatalf("obsolete flag accepted or unclear error: %v\n%s", err, output)
			}
		})
	}

	t.Run("default-and-explicit-v3-export", func(t *testing.T) {
		var previousJSON interface{}
		var previousBinary []byte
		for _, explicit := range []bool{false, true} {
			name := "default"
			if explicit {
				name = "explicit"
			}
			jsonOutput, pbOutput := filepath.Join(dir, name+".json"), filepath.Join(dir, name+".pbb")
			args := []string{"-index=Index.csv", "-proto_desc=schema.pb", "-proto_map=mapping.json", "-pbjson_out=" + jsonOutput, "-pbbin_out=" + pbOutput}
			if explicit {
				args = append(args, "-mode=v3")
			}
			cmd := exec.Command(binary, args...)
			cmd.Dir = filepath.Join(root, "v3", "example", "existingproto")
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%s export: %v\n%s", name, err, output)
			}
			jsonData, err := ioutil.ReadFile(jsonOutput)
			if err != nil {
				t.Fatal(err)
			}
			var decoded interface{}
			if err := json.Unmarshal(jsonData, &decoded); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(jsonData), "新手剑") || !strings.Contains(string(jsonData), "18446744073709551615") {
				t.Fatalf("missing example values: %s", jsonData)
			}
			pbData, err := ioutil.ReadFile(pbOutput)
			if err != nil || len(pbData) == 0 {
				t.Fatalf("missing Protobuf export: %v", err)
			}
			if explicit && (!reflect.DeepEqual(decoded, previousJSON) || !bytes.Equal(pbData, previousBinary)) {
				t.Fatal("default export differs from explicit V3")
			}
			previousJSON, previousBinary = decoded, pbData
		}
	})
}
