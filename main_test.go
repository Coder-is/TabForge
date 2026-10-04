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
	"strconv"
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
	t.Run("portable-project-init-and-discovery", func(t *testing.T) {
		projectRoot := filepath.Join(dir, "new project 中文 with spaces")
		cmd := exec.Command(binary, "-init="+projectRoot)
		cmd.Env = append(os.Environ(), "PATH=")
		if output, err := cmd.CombinedOutput(); err != nil || !bytes.Contains(output, []byte("导出成功")) {
			t.Fatalf("portable initialization: %v\n%s", err, output)
		}
		for _, name := range []string{"tabforge.json", "Tables/Items.xlsx", "Generated/schema/go/config.pb.go", "Generated/data/tables.pbb", "Generated/data/tables-map.json", "Tools/TabForge/Export.command", "Tools/TabForge/Export.bat"} {
			if _, err := os.Stat(filepath.Join(projectRoot, filepath.FromSlash(name))); err != nil {
				t.Fatal(err)
			}
		}
		toolName := "tabforge"
		if runtime.GOOS == "windows" {
			toolName += ".exe"
		}
		portable := filepath.Join(projectRoot, "Tools", "TabForge", toolName)
		for _, cwd := range []string{filepath.Join(projectRoot, "Tables"), t.TempDir()} {
			cmd := exec.Command(portable)
			cmd.Dir = cwd
			cmd.Env = append(os.Environ(), "PATH=")
			if output, err := cmd.CombinedOutput(); err != nil || !bytes.Contains(output, []byte("导出成功")) {
				t.Fatalf("project discovery from %s: %v\n%s", cwd, err, output)
			}
		}
		original, err := os.ReadFile(filepath.Join(projectRoot, "Tables", "Items.xlsx"))
		if err != nil {
			t.Fatal(err)
		}
		if output, err := exec.Command(binary, "-init="+projectRoot).CombinedOutput(); err == nil || !bytes.Contains(output, []byte("overwrite")) {
			t.Fatalf("existing files overwritten: %v\n%s", err, output)
		}
		current, err := os.ReadFile(filepath.Join(projectRoot, "Tables", "Items.xlsx"))
		if err != nil || !bytes.Equal(original, current) {
			t.Fatal("initialization changed source files")
		}
		if output, err := exec.Command(binary, "-project="+projectRoot, "-index=Index.csv").CombinedOutput(); err == nil {
			t.Fatalf("project accepted conflicting flags: %s", output)
		}
		// Copying into an independent Go module must not depend on example
		// messages compiled into this repository. Use the consumer's own imports.
		if err := filepath.WalkDir(filepath.Join(projectRoot, "Protocols"), func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || filepath.Ext(path) != ".proto" {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return os.WriteFile(path, bytes.ReplaceAll(data, []byte("github.com/Coder-is/TabForge/examples/complete/Generated"), []byte("example.com/portable/Generated")), 0644)
		}); err != nil {
			t.Fatal(err)
		}
		clientPath := filepath.Join(projectRoot, "Clients", "go", "main.go")
		clientSource, err := os.ReadFile(clientPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(clientPath, bytes.ReplaceAll(clientSource, []byte("github.com/Coder-is/TabForge/examples/complete/Generated"), []byte("example.com/portable/Generated")), 0644); err != nil {
			t.Fatal(err)
		}
		if output, err := exec.Command(portable, "-project="+projectRoot).CombinedOutput(); err != nil {
			t.Fatalf("consumer regeneration: %v\n%s", err, output)
		}
		module := "module example.com/portable\n\ngo 1.26.6\n\nrequire github.com/Coder-is/TabForge v0.0.0\n\nreplace github.com/Coder-is/TabForge => " + strconv.Quote(filepath.ToSlash(root)) + "\n"
		if err := os.WriteFile(filepath.Join(projectRoot, "go.mod"), []byte(module), 0644); err != nil {
			t.Fatal(err)
		}
		consumer := exec.Command("go", "run", "-mod=mod", "./Clients/go", projectRoot)
		consumer.Dir = projectRoot
		if output, err := consumer.CombinedOutput(); err != nil || !bytes.Contains(output, []byte("18446744073709551615")) {
			t.Fatalf("independent Go consumer: %v\n%s", err, output)
		}
	})
	t.Run("protocol-bundle-and-reload", func(t *testing.T) {
		outputDir := filepath.Join(dir, "protocol")
		manifest := filepath.Join(root, "examples", "protocol", "contract.json")
		output, err := exec.Command(binary, "-protocol="+manifest, "-protocol_out="+outputDir).CombinedOutput()
		if err != nil || !bytes.Contains(output, []byte("2 endpoints")) || !bytes.Contains(output, []byte("Schema hash: ")) {
			t.Fatalf("protocol generation: %v\n%s", err, output)
		}
		for _, name := range []string{"types.ts", "contract.json", "schema.pb", "PROTOCOL.md", "protocol.gd", "wire_schema.json", "runtime.json"} {
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
	t.Run("editor-export-check-import-and-report", func(t *testing.T) {
		source := filepath.Join(dir, "second version 中文")
		if output, err := exec.Command(binary, "-init="+source, "-report").CombinedOutput(); err != nil {
			t.Fatalf("init: %v\n%s", err, output)
		}
		manifest := filepath.Join(source, "Generated", "export.json")
		before, err := os.ReadFile(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if output, err := exec.Command(binary, "-project="+source, "-check", "-report").CombinedOutput(); err != nil {
			t.Fatalf("check: %v\n%s", err, output)
		}
		after, err := os.ReadFile(manifest)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("check replaced the published bundle")
		}
		for _, kind := range []string{"unity", "cocos", "godot"} {
			game := filepath.Join(dir, "game 中文 "+kind)
			if err := os.MkdirAll(filepath.Join(game, map[string]string{"unity": "Assets", "cocos": "assets", "godot": "scripts"}[kind]), 0755); err != nil {
				t.Fatal(err)
			}
			if kind == "godot" {
				if err := os.WriteFile(filepath.Join(game, "project.godot"), []byte("config_version=5\n"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			args := []string{"-import=" + filepath.Join(source, "Generated"), "-editor=" + kind, "-editor_project=" + game, "-report"}
			cmd := exec.Command(binary, args...)
			cmd.Env = append(os.Environ(), "PATH=")
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%s import: %v\n%s", kind, err, output)
			}
			data, err := os.ReadFile(filepath.Join(game, ".tabforge-report.json"))
			if err != nil {
				t.Fatal(err)
			}
			var report struct {
				Success      bool   `json:"success"`
				EditorOutput string `json:"editorOutput"`
			}
			if err := json.Unmarshal(data, &report); err != nil || !report.Success || report.EditorOutput == "" {
				t.Fatalf("import report: %v %s", err, data)
			}
		}
		if err := os.WriteFile(filepath.Join(source, "Protocols", "bad.proto"), []byte("syntax = \"proto3\";\nmessage Bad { string name = ; }"), 0644); err != nil {
			t.Fatal(err)
		}
		if output, err := exec.Command(binary, "-project="+source, "-check", "-report").CombinedOutput(); err == nil {
			t.Fatalf("invalid input passed: %s", output)
		}
		data, err := os.ReadFile(filepath.Join(source, ".tabforge-report.json"))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(data, []byte(`"success": false`)) || !bytes.Contains(data, []byte(`"code": "proto_compile"`)) {
			t.Fatalf("failed report: %s", data)
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
