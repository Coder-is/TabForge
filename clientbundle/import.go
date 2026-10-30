// Package clientbundle imports self-contained exports into engine asset trees.
package clientbundle

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Coder-is/TabForge/databundle"
	"github.com/Coder-is/TabForge/internal/filelock"
	"github.com/Coder-is/TabForge/internal/publish"
	"github.com/Coder-is/TabForge/project"
	"github.com/Coder-is/TabForge/protocol"
	"github.com/Coder-is/TabForge/sdk"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

// markerName identifies a directory that TabForge owns and may replace.
const markerName = ".tabforge-client.json"

type Result struct {
	Kind       string   `json:"kind"`
	SchemaHash string   `json:"schemaHash"`
	Output     string   `json:"output"`
	Files      []string `json:"files"`
	// Warnings are non-fatal problems, such as a superseded backup that could
	// not be deleted after publishing; the next import retries the cleanup.
	Warnings []string `json:"-"`
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
	case "unreal":
		relative = "Content/TabForgeGenerated"
		projects, err := filepath.Glob(filepath.Join(engineRoot, "*.uproject"))
		if err != nil || len(projects) != 1 {
			return nil, fmt.Errorf("Unreal project needs exactly one .uproject file")
		}
	default:
		return nil, fmt.Errorf("unknown editor %q (unity, cocos, godot or unreal)", kind)
	}
	out, err := safePath(engineRoot, relative)
	if err != nil {
		return nil, err
	}
	if contains(out, source) || contains(source, out) {
		return nil, fmt.Errorf("import source and engine output must not overlap")
	}
	lockPath := filepath.Join(engineRoot, ".tabforge-client.lock")
	lock, err := filelock.Acquire(lockPath)
	if err != nil {
		return nil, fmt.Errorf("cannot lock engine import: %w", err)
	}
	defer lock.Release()
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
	// Apply the runtime loader's checks (checksums, descriptor and wire schema)
	// so the engine never receives a bundle that databundle.Open would reject.
	// First-version exports have neither the checksum manifest nor data entries;
	// they still import structure only.
	var checksums map[string]databundle.Entry
	if declared[databundle.ManifestName] {
		if _, err := databundle.Open(source, manifest.SchemaHash); err != nil {
			return nil, fmt.Errorf("verify %s: %w", databundle.ManifestName, err)
		}
		if checksums, err = readChecksums(source); err != nil {
			return nil, err
		}
	} else if len(manifest.Data) > 0 {
		return nil, fmt.Errorf("bundle has data but no %s; export it again to import table data", databundle.ManifestName)
	}
	// Bytes are checked again where they are read, so a file replaced after the
	// bundle check cannot slip into the engine tree.
	verified := func(name string) ([]byte, error) {
		entry, ok := checksums[name]
		if !ok && checksums != nil {
			return nil, fmt.Errorf("%s is not covered by %s", name, databundle.ManifestName)
		}
		path, err := safePath(source, name)
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if ok && databundle.Digest(data) != entry.SHA256 {
			return nil, fmt.Errorf("checksum mismatch in %s: %s", databundle.ManifestName, name)
		}
		return data, nil
	}
	descriptor, err := verified("schema/schema.pb")
	if err != nil {
		return nil, err
	}
	set := new(descriptorpb.FileDescriptorSet)
	if err := proto.Unmarshal(descriptor, set); err != nil {
		return nil, fmt.Errorf("decode schema: %w", err)
	}
	schema, err := protocol.NewSchema(set)
	if err != nil {
		return nil, err
	}
	if schema.Fingerprint() != manifest.SchemaHash {
		return nil, fmt.Errorf("schema hash does not match export.json")
	}
	if !check {
		// Repair an import interrupted during publishing before inspecting out.
		if err := publish.Recover(out, markerName); err != nil {
			return nil, err
		}
	}
	if stat, err := os.Lstat(out); err == nil {
		if !stat.IsDir() {
			return nil, fmt.Errorf("generated asset destination is not a directory")
		}
		marker, err := os.ReadFile(filepath.Join(out, markerName))
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
	// Engine asset trees are case-insensitive on Windows and macOS; a data file must
	// never silently replace a runtime file such as wire_schema.json or the marker.
	written := map[string]bool{}
	write := func(name string, data []byte) error {
		path, err := safePath(stage, name)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		key := strings.ToLower(path)
		if written[key] {
			return fmt.Errorf("client asset path collides with another generated file: %s", name)
		}
		written[key] = true
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
		// Non-canonical paths such as "data/../wire_schema.json" would bypass the
		// runtime-name checks below.
		lower := strings.ToLower(entry.Path)
		if path.Clean(entry.Path) != entry.Path || !strings.HasSuffix(entry.Path, ".json") || strings.HasPrefix(lower, ".tabforge-") || seenData[lower] {
			return nil, fmt.Errorf("invalid or repeated client data path: %s", entry.Path)
		}
		seenData[lower] = true
		// Keep original paths so clients can refer to multiple data roots.
		if strings.HasPrefix(lower, "schema/") || strings.HasPrefix(lower, "runtime/") || lower == "wire_schema.json" || (kind == "unreal" && lower == "bundle.json") {
			return nil, fmt.Errorf("data path conflicts with client runtime: %s", entry.Path)
		}
		if !declared[entry.Path] {
			return nil, fmt.Errorf("data file not declared: %s", entry.Path)
		}
		if sum := checksums[entry.Path]; sum.Encoding != "protojson" || sum.Message != entry.Message {
			return nil, fmt.Errorf("data file %s does not match its %s entry", entry.Path, databundle.ManifestName)
		}
		data, err := verified(entry.Path)
		if err != nil {
			return nil, err
		}
		if _, err := schema.DecodeJSON(entry.Message, data); err != nil {
			return nil, fmt.Errorf("%s: %w", entry.Path, err)
		}
		if err := write(filepath.ToSlash(filepath.Join(resource, entry.Path)), data); err != nil {
			return nil, err
		}
	}
	switch kind {
	case "unreal":
		bundle := struct {
			Format     string             `json:"format"`
			SchemaHash string             `json:"schemaHash"`
			Data       []project.DataFile `json:"data"`
		}{"tabforge.unreal.data.v1", manifest.SchemaHash, append([]project.DataFile{}, manifest.Data...)}
		data, err := json.MarshalIndent(bundle, "", "  ")
		if err != nil {
			return nil, err
		}
		if err := write("bundle.json", append(data, '\n')); err != nil {
			return nil, err
		}
	case "cocos":
		for _, name := range []string{"data.ts", "schema.ts", "json.ts"} {
			data, err := sdk.ReadClientAsset("typescript/" + name)
			if err != nil {
				return nil, err
			}
			data = []byte(strings.ReplaceAll(string(data), "./schema.ts", "./schema"))
			data = []byte(strings.ReplaceAll(string(data), "./json.ts", "./json"))
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
			data, err := sdk.ReadClientAsset("unity/Runtime/" + name)
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
			data, err := sdk.ReadClientAsset("godot/" + name)
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
	if err := write(markerName, append(marker, '\n')); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !check {
		warning, err := publish.Dir(stage, out, markerName)
		if err != nil {
			return nil, err
		}
		if warning != nil {
			r.Warnings = append(r.Warnings, warning.Error())
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

// readChecksums indexes data_manifest.json by path. databundle.Open has already
// validated its structure, paths and digests.
func readChecksums(source string) (map[string]databundle.Entry, error) {
	path, err := safePath(source, databundle.ManifestName)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m databundle.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", databundle.ManifestName, err)
	}
	checksums := map[string]databundle.Entry{
		m.Descriptor.Path: {File: m.Descriptor},
		m.WireSchema.Path: {File: m.WireSchema},
	}
	for _, entry := range m.Data {
		checksums[entry.Path] = entry
	}
	return checksums, nil
}
