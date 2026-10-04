// Package clientbundle imports self-contained exports into engine asset trees.
package clientbundle

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Coder-is/TabForge/project"
	"github.com/Coder-is/TabForge/protocol"
	"github.com/Coder-is/TabForge/sdk"
)

type Result struct {
	Kind       string   `json:"kind"`
	SchemaHash string   `json:"schemaHash"`
	Output     string   `json:"output"`
	Files      []string `json:"files"`
}

// Import prepares and validates the whole bundle, then replaces only the
// marked generated directory. Check mode never publishes engine assets.
func Import(ctx context.Context, source, engineRoot, kind string, check bool) (*Result, error) {
	source, err := filepath.Abs(source)
	if err != nil {
		return nil, err
	}
	engineRoot, err = filepath.Abs(engineRoot)
	if err != nil {
		return nil, err
	}
	var relative, resource string
	switch kind {
	case "unity":
		relative = "Assets/TabForgeGenerated"
		resource = "Resources/TabForge"
		if stat, err := os.Stat(filepath.Join(engineRoot, "Assets")); err != nil || !stat.IsDir() {
			return nil, fmt.Errorf("Unity project needs an Assets directory")
		}
	case "cocos":
		relative = "assets/resources/tabforge"
		if stat, err := os.Stat(filepath.Join(engineRoot, "assets")); err != nil || !stat.IsDir() {
			return nil, fmt.Errorf("Cocos project needs an assets directory")
		}
	case "godot":
		relative = "tabforge_generated"
		if _, err := os.Stat(filepath.Join(engineRoot, "project.godot")); err != nil {
			return nil, fmt.Errorf("Godot project needs project.godot: %w", err)
		}
	default:
		return nil, fmt.Errorf("unknown editor %q (unity, cocos or godot)", kind)
	}
	out, err := safePath(engineRoot, relative)
	if err != nil {
		return nil, err
	}
	if contains(out, source) || contains(source, out) {
		return nil, fmt.Errorf("import source and engine output must not overlap")
	}
	lockPath := filepath.Join(engineRoot, ".tabforge-client.lock")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("cannot lock engine import: %w", err)
	}
	defer lock.Close()
	defer os.Remove(lockPath)
	if _, err := fmt.Fprintln(lock, os.Getpid()); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	manifestPath, err := safePath(source, "export.json")
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, err
	}
	if err := protocol.ValidateJSON(data); err != nil {
		return nil, err
	}
	var manifest project.Result
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}
	if manifest.Format != "tabforge.export.v1" || manifest.SchemaHash == "" {
		return nil, fmt.Errorf("import a TabForge export with schema, not raw source files")
	}
	declared := map[string]bool{}
	for _, name := range manifest.Files {
		if _, err := safePath(source, name); err != nil {
			return nil, err
		}
		declared[name] = true
	}
	if !declared["schema/schema.pb"] || !declared["schema/types.ts"] {
		return nil, fmt.Errorf("bundle is missing generated schema files")
	}
	schemaPath, err := safePath(source, "schema/schema.pb")
	if err != nil {
		return nil, err
	}
	schema, err := protocol.LoadSchema(schemaPath)
	if err != nil {
		return nil, err
	}
	if schema.Fingerprint() != manifest.SchemaHash {
		return nil, fmt.Errorf("schema hash does not match export.json")
	}
	if stat, err := os.Lstat(out); err == nil {
		if !stat.IsDir() {
			return nil, fmt.Errorf("generated asset destination is not a directory")
		}
		marker, err := os.ReadFile(filepath.Join(out, ".tabforge-client.json"))
		if err != nil {
			return nil, fmt.Errorf("refusing to replace an unmarked asset directory: %s", out)
		}
		var previous Result
		if json.Unmarshal(marker, &previous) != nil || previous.Kind != kind {
			return nil, fmt.Errorf("invalid engine import marker: %s", out)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0755); err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp(filepath.Dir(out), ".tabforge-client-stage-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	write := func(name string, data []byte) error {
		path, err := safePath(stage, name)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		return os.WriteFile(path, data, 0644)
	}
	wire, err := json.MarshalIndent(schema.WireSchema(), "", "  ")
	if err != nil {
		return nil, err
	}
	if err := write(filepath.ToSlash(filepath.Join(resource, "wire_schema.json")), append(wire, '\n')); err != nil {
		return nil, err
	}
	seenData := map[string]bool{}
	for _, entry := range manifest.Data {
		if !strings.HasSuffix(entry.Path, ".json") || strings.HasPrefix(entry.Path, ".tabforge-") || seenData[strings.ToLower(entry.Path)] {
			return nil, fmt.Errorf("invalid or repeated client data path: %s", entry.Path)
		}
		seenData[strings.ToLower(entry.Path)] = true
		if !declared[entry.Path] {
			return nil, fmt.Errorf("data file not declared: %s", entry.Path)
		}
		path, err := safePath(source, entry.Path)
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if _, err := schema.DecodeJSON(entry.Message, data); err != nil {
			return nil, fmt.Errorf("%s: %w", entry.Path, err)
		}
		// Keep original paths so clients can refer to multiple data roots.
		if strings.HasPrefix(entry.Path, "schema/") || strings.HasPrefix(entry.Path, "Runtime/") || entry.Path == "wire_schema.json" {
			return nil, fmt.Errorf("data path conflicts with client runtime: %s", entry.Path)
		}
		if err := write(filepath.ToSlash(filepath.Join(resource, entry.Path)), data); err != nil {
			return nil, err
		}
	}
	switch kind {
	case "cocos":
		for _, name := range []string{"data.ts", "schema.ts"} {
			data, err := sdk.ClientAssets.ReadFile("typescript/" + name)
			if err != nil {
				return nil, err
			}
			data = []byte(strings.ReplaceAll(string(data), "./schema.ts", "./schema"))
			if err := write(name, data); err != nil {
				return nil, err
			}
		}
		data, err := schema.TypeScriptDataTypes()
		if err != nil {
			return nil, err
		}
		if err := write("types.ts", data); err != nil {
			return nil, err
		}
	case "unity":
		for _, name := range []string{"DataSchema.cs", "WireValidator.cs", "JsonSyntax.cs"} {
			data, err := sdk.ClientAssets.ReadFile("unity/Runtime/" + name)
			if err != nil {
				return nil, err
			}
			text := strings.ReplaceAll(string(data), "TabForge.Protocol", "TabForge.Data")
			text = strings.ReplaceAll(text, "ProtocolException", "DataException")
			if err := write("Runtime/"+name, []byte(text)); err != nil {
				return nil, err
			}
		}
		if err := write("Runtime/DataTypes.cs", []byte(schema.CSharpDataTypes())); err != nil {
			return nil, err
		}
		if err := write("Runtime/DataException.cs", []byte("using System;\nnamespace TabForge.Data { public sealed class DataException : Exception { public string Code { get; } public DataException(string code, string message) : base(message) { Code = code; } } }\n")); err != nil {
			return nil, err
		}
		if err := write("Runtime/TabForge.Data.asmdef", []byte(`{"name":"TabForge.Data","references":["Unity.Newtonsoft.Json"],"autoReferenced":true}`)); err != nil {
			return nil, err
		}
	case "godot":
		for _, name := range []string{"data_schema.gd", "wire_validator.gd"} {
			data, err := sdk.ClientAssets.ReadFile("godot/" + name)
			if err != nil {
				return nil, err
			}
			if err := write(name, data); err != nil {
				return nil, err
			}
		}
		if err := write("schema.gd", []byte("# Generated ProtoJSON schema\nextends RefCounted\nconst SCHEMA_HASH = "+fmt.Sprintf("%q", manifest.SchemaHash)+"\nconst WIRE_SCHEMA = "+string(wire)+"\n")); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Retain stable engine identities for assets that survive regeneration.
	if err := preserveMetadata(out, stage); err != nil {
		return nil, err
	}
	r := &Result{Kind: kind, SchemaHash: manifest.SchemaHash, Output: out, Files: []string{}}
	if err := filepath.WalkDir(stage, func(path string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !e.IsDir() {
			name, err := filepath.Rel(stage, path)
			if err != nil {
				return err
			}
			r.Files = append(r.Files, filepath.ToSlash(name))
		}
		return nil
	}); err != nil {
		return nil, err
	}
	sort.Strings(r.Files)
	marker, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := write(".tabforge-client.json", append(marker, '\n')); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !check {
		if err := publish(stage, out); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func contains(parent, child string) bool {
	rel, err := filepath.Rel(strings.ToLower(parent), strings.ToLower(child))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
func safePath(root, name string) (string, error) {
	if name == "" || strings.ContainsAny(name, "\\:") || filepath.IsAbs(name) {
		return "", fmt.Errorf("nonportable bundle path: %q", name)
	}
	clean := filepath.Clean(filepath.FromSlash(name))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("bundle path escapes directory: %q", name)
	}
	current := root
	// Check root as well as children; an engine asset tree must not be a symlink.
	for _, part := range append([]string{""}, strings.Split(clean, string(filepath.Separator))...) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("symlink bundle path: %s", current)
		}
	}
	return filepath.Join(root, clean), nil
}
func preserveMetadata(out, stage string) error {
	if _, err := os.Stat(out); os.IsNotExist(err) {
		return nil
	}
	return filepath.WalkDir(out, func(path string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink in generated assets: %s", path)
		}
		if e.IsDir() {
			return nil
		}
		ext := filepath.Ext(path)
		if ext != ".meta" && ext != ".uid" {
			return nil
		}
		rel, err := filepath.Rel(out, path)
		if err != nil {
			return err
		}
		target := filepath.Join(stage, rel)
		if _, err := os.Stat(strings.TrimSuffix(target, ext)); err != nil {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0644)
	})
}
func publish(stage, out string) error {
	backup := out + ".tabforge-backup"
	if _, err := os.Lstat(backup); err == nil {
		return fmt.Errorf("previous import backup exists: %s", backup)
	} else if !os.IsNotExist(err) {
		return err
	}
	exists := false
	if _, err := os.Stat(out); err == nil {
		exists = true
		if err := os.Rename(out, backup); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(stage, out); err != nil {
		if exists {
			if rollback := os.Rename(backup, out); rollback != nil {
				return fmt.Errorf("import failed: %v; rollback failed: %v; recover %s", err, rollback, backup)
			}
		}
		return err
	}
	if exists {
		return os.RemoveAll(backup)
	}
	return nil
}
