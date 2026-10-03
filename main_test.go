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
	t.Run("protocol-bundle-and-reload", func(t *testing.T) {
		outputDir := filepath.Join(dir, "protocol")
		manifest := filepath.Join(root, "examples", "protocol", "contract.json")
		output, err := exec.Command(binary, "-protocol="+manifest, "-protocol_out="+outputDir).CombinedOutput()
		if err != nil || !bytes.Contains(output, []byte("2 endpoints")) || !bytes.Contains(output, []byte("Schema hash: ")) {
			t.Fatalf("protocol generation: %v\n%s", err, output)
		}
		for _, name := range []string{"types.ts", "contract.json", "schema.pb", "PROTOCOL.md", "protocol.gd", "wire_schema.json"} {
			if data, err := ioutil.ReadFile(filepath.Join(outputDir, name)); err != nil || len(data) == 0 {
				t.Fatalf("missing %s: %v", name, err)
			}
		}
		output, err = exec.Command(binary, "-protocol="+filepath.Join(outputDir, "contract.json")).CombinedOutput()
		if err != nil {
			t.Fatalf("distributed bundle cannot be validated: %v\n%s", err, output)
		}
		output, err = exec.Command(binary, "-protocol="+manifest, "-protocol_against="+filepath.Join(outputDir, "contract.json")).CombinedOutput()
		if err != nil || !bytes.Contains(output, []byte("No breaking changes")) {
			t.Fatalf("identical distributed contract rejected: %v\n%s", err, output)
		}
		data, err := os.ReadFile(manifest)
		if err != nil {
			t.Fatal(err)
		}
		var changed map[string]interface{}
		if err := json.Unmarshal(data, &changed); err != nil {
			t.Fatal(err)
		}
		changed["descriptor"] = filepath.Join(root, "examples", "protocol", "schema.pb")
		changed["endpoints"].([]interface{})[0].(map[string]interface{})["path"] = "/v2/chat/complete"
		data, err = json.Marshal(changed)
		if err != nil {
			t.Fatal(err)
		}
		changedPath := filepath.Join(dir, "changed-contract.json")
		if err := os.WriteFile(changedPath, data, 0600); err != nil {
			t.Fatal(err)
		}
		rejectedDir := filepath.Join(dir, "rejected-bundle")
		output, err = exec.Command(binary, "-protocol="+changedPath, "-protocol_against="+manifest, "-protocol_out="+rejectedDir).CombinedOutput()
		if err == nil || !bytes.Contains(output, []byte("BREAKING")) {
			t.Fatalf("breaking route accepted: %v\n%s", err, output)
		}
		if _, err := os.Stat(rejectedDir); !os.IsNotExist(err) {
			t.Fatalf("breaking contract wrote output: %v", err)
		}
		for _, args := range [][]string{
			{"-protocol_out=" + outputDir},
			{"-protocol_against=" + manifest},
			{"-protocol=" + manifest, "-index=Index.csv"},
		} {
			if output, err := exec.Command(binary, args...).CombinedOutput(); err == nil {
				t.Fatalf("invalid protocol invocation accepted: %v\n%s", args, output)
			}
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
			args := []string{"-index=Index.csv", "-proto_desc=schema.pb", "-proto_map=mapping.json", "-combinename=Item", "-package=not.a.valid-go-name", "-pbjson_out=" + jsonOutput, "-pbbin_out=" + pbOutput}
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
