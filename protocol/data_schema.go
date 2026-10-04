package protocol

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Coder-is/TabForge/sdk/typescript"
	"github.com/bufbuild/protocompile"
	gengo "google.golang.org/protobuf/cmd/protoc-gen-go/internal_gengo"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"
)

// Schema describes data without requiring RPCs, routes, or a network transport.
// It is immutable after construction and safe to share between readers.
type Schema struct {
	contract *Contract
	entries  []string
}

// CompileSchema compiles Proto sources and their imports in-process. File names
// are slash-separated paths relative to importDirs; standard imports are built in.
func CompileSchema(ctx context.Context, importDirs, files []string) (*Schema, error) {
	if len(files) == 0 {
		return nil, fmt.Errorf("no Proto source files")
	}
	source := &protocompile.SourceResolver{ImportPaths: importDirs}
	resolver := protocompile.ResolverFunc(func(name string) (protocompile.SearchResult, error) {
		if !localProtoPath(name) {
			return protocompile.SearchResult{}, fmt.Errorf("Proto paths must be relative slash-separated paths without '..': %q", name)
		}
		return source.FindFileByPath(name)
	})
	compiler := protocompile.Compiler{
		Resolver:       protocompile.WithStandardImports(resolver),
		SourceInfoMode: protocompile.SourceInfoStandard,
	}
	compiled, err := compiler.Compile(ctx, files...)
	if err != nil {
		return nil, fmt.Errorf("compile Proto: %w", err)
	}
	set := &descriptorpb.FileDescriptorSet{}
	seen := map[string]bool{}
	var visit func(protoreflect.FileDescriptor)
	visit = func(f protoreflect.FileDescriptor) {
		if seen[f.Path()] {
			return
		}
		seen[f.Path()] = true
		for i := 0; i < f.Imports().Len(); i++ {
			visit(f.Imports().Get(i).FileDescriptor)
		}
		set.File = append(set.File, protodesc.ToFileDescriptorProto(f))
	}
	for _, f := range compiled {
		visit(f)
	}
	s, err := NewSchema(set)
	if err != nil {
		return nil, err
	}
	s.entries = append([]string(nil), files...)
	sort.Strings(s.entries)
	return s, nil
}

// NewSchema constructs a data schema from a self-contained descriptor set.
func NewSchema(set *descriptorpb.FileDescriptorSet) (*Schema, error) {
	if set == nil || len(set.File) == 0 {
		return nil, fmt.Errorf("empty descriptor set")
	}
	files, err := protodesc.NewFiles(set)
	if err != nil {
		return nil, fmt.Errorf("resolve schema: %w", err)
	}
	c := &Contract{Files: files, Types: new(protoregistry.Types)}
	files.RangeFiles(func(f protoreflect.FileDescriptor) bool {
		err = c.register(f.Messages())
		return err == nil
	})
	if err != nil {
		return nil, err
	}
	return &Schema{contract: c}, nil
}

func LoadSchema(path string) (*Schema, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	set := new(descriptorpb.FileDescriptorSet)
	if err := proto.Unmarshal(data, set); err != nil {
		return nil, fmt.Errorf("decode schema: %w", err)
	}
	return NewSchema(set)
}

func (s *Schema) Fingerprint() string                  { return s.contract.Fingerprint() }
func (s *Schema) TypeScriptDataTypes() ([]byte, error) { return s.contract.typeScript(false) }
func (s *Schema) WireSchema() WireSchema               { return s.contract.WireSchema() }
func (s *Schema) DescriptorSet() *descriptorpb.FileDescriptorSet {
	return s.contract.descriptorSet(true)
}

func (s *Schema) NewMessage(name string) (proto.Message, error) {
	m, err := s.contract.Types.FindMessageByName(protoreflect.FullName(name))
	if err != nil {
		return nil, fmt.Errorf("unknown message %q: %w", name, err)
	}
	return m.New().Interface(), nil
}

func (s *Schema) DecodeJSON(name string, data []byte) (proto.Message, error) {
	m, err := s.NewMessage(name)
	if err != nil {
		return nil, err
	}
	if err := ValidateJSON(data); err != nil {
		return nil, err
	}
	err = (protojson.UnmarshalOptions{Resolver: s.contract.Types}).Unmarshal(data, m)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", name, err)
	}
	return m, nil
}

func (s *Schema) DecodeBinary(name string, data []byte) (proto.Message, error) {
	m, err := s.NewMessage(name)
	if err != nil {
		return nil, err
	}
	if err := (proto.UnmarshalOptions{Resolver: s.contract.Types}).Unmarshal(data, m); err != nil {
		return nil, fmt.Errorf("decode %s: %w", name, err)
	}
	return m, nil
}

// ReadFile reads ProtoJSON or Protobuf. TabForge's legacy .bin is not Protobuf.
func (s *Schema) ReadFile(name, path string) (proto.Message, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		return s.DecodeJSON(name, data)
	case ".pbb", ".pb":
		return s.DecodeBinary(name, data)
	default:
		return nil, fmt.Errorf("unsupported data extension %q; use .json, .pbb or .pb", filepath.Ext(path))
	}
}

// Generate writes a pure data bundle. Enable Go code only when application Proto
// files declare go_package. Both compilation and Go generation are built in.
func (s *Schema) Generate(dir string, goCode bool) error {
	ts, err := s.contract.typeScript(false)
	if err != nil {
		return err
	}
	pb, err := proto.MarshalOptions{Deterministic: true}.Marshal(s.DescriptorSet())
	if err != nil {
		return err
	}
	wire, err := json.MarshalIndent(s.WireSchema(), "", "  ")
	if err != nil {
		return err
	}
	metadata, err := json.MarshalIndent(struct {
		Format  string   `json:"format"`
		Hash    string   `json:"schemaHash"`
		Entries []string `json:"entries,omitempty"`
	}{"tabforge.schema.v1", s.Fingerprint(), s.entries}, "", "  ")
	if err != nil {
		return err
	}
	if err := ensureDir(dir); err != nil {
		return err
	}
	for name, data := range map[string][]byte{
		"schema.pb": pb, "types.ts": ts, "wire_schema.json": append(wire, '\n'),
		"bundle.json": append(metadata, '\n'), "SCHEMA.md": []byte(s.markdown()),
	} {
		if err := atomicWrite(filepath.Join(dir, name), data); err != nil {
			return err
		}
	}
	for _, name := range []string{"data.ts", "schema.ts"} {
		data, err := typescript.DataFiles.ReadFile(name)
		if err != nil {
			return err
		}
		if err := atomicWrite(filepath.Join(dir, name), data); err != nil {
			return err
		}
	}
	if goCode {
		return s.generateGo(filepath.Join(dir, "go"))
	}
	return nil
}

func (c *Contract) allTypes() ([]protoreflect.MessageDescriptor, []protoreflect.EnumDescriptor) {
	var messages []protoreflect.MessageDescriptor
	var enums []protoreflect.EnumDescriptor
	var collect func(protoreflect.MessageDescriptors)
	collect = func(list protoreflect.MessageDescriptors) {
		for i := 0; i < list.Len(); i++ {
			m := list.Get(i)
			if !m.IsMapEntry() {
				messages = append(messages, m)
			}
			for j := 0; j < m.Enums().Len(); j++ {
				enums = append(enums, m.Enums().Get(j))
			}
			collect(m.Messages())
		}
	}
	c.Files.RangeFiles(func(f protoreflect.FileDescriptor) bool {
		collect(f.Messages())
		for i := 0; i < f.Enums().Len(); i++ {
			enums = append(enums, f.Enums().Get(i))
		}
		return true
	})
	sort.Slice(messages, func(i, j int) bool { return messages[i].FullName() < messages[j].FullName() })
	sort.Slice(enums, func(i, j int) bool { return enums[i].FullName() < enums[j].FullName() })
	return messages, enums
}

func (s *Schema) markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Data schema\n\nSchema hash: `%s`. No RPC or transport is required.\n\n", s.Fingerprint())
	b.WriteString("Types describe ProtoJSON: decimal strings for int64/uint64, Base64 bytes, enum names, and omitted defaults. oneof members are mutually exclusive.\n\n")
	messages, enums := s.contract.allTypes()
	for _, m := range messages {
		fmt.Fprintf(&b, "## %s\n\n%s\n\n", m.FullName(), md(sourceComment(m)))
		if special := wellKnown(m.FullName()); special != "" {
			fmt.Fprintf(&b, "ProtoJSON: `%s`.\n\n", special)
			continue
		}
		b.WriteString("| Field | Number | Type | Presence | Description |\n| --- | --- | --- | --- | --- |\n")
		for i := 0; i < m.Fields().Len(); i++ {
			f := m.Fields().Get(i)
			presence := "default may be omitted"
			if f.HasPresence() {
				presence = "explicit presence"
			}
			if o := f.ContainingOneof(); o != nil && !o.IsSynthetic() {
				presence = "oneof " + string(o.Name())
			}
			fmt.Fprintf(&b, "| %s | %d | %s | %s | %s |\n", md(f.JSONName()), f.Number(), md(protoFieldType(f)), md(presence), md(sourceComment(f)))
		}
		b.WriteString("\n")
	}
	for _, e := range enums {
		fmt.Fprintf(&b, "## %s\n\n| Name | Number |\n| --- | --- |\n", e.FullName())
		for i := 0; i < e.Values().Len(); i++ {
			v := e.Values().Get(i)
			fmt.Fprintf(&b, "| %s | %d |\n", v.Name(), v.Number())
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (s *Schema) generateGo(dir string) error {
	// protogen expects imported files before their importers.
	set := &descriptorpb.FileDescriptorSet{}
	seen := map[string]bool{}
	var visit func(protoreflect.FileDescriptor)
	visit = func(f protoreflect.FileDescriptor) {
		if seen[f.Path()] {
			return
		}
		seen[f.Path()] = true
		for i := 0; i < f.Imports().Len(); i++ {
			visit(f.Imports().Get(i).FileDescriptor)
		}
		set.File = append(set.File, protodesc.ToFileDescriptorProto(f))
	}
	var entries []string
	s.contract.Files.RangeFiles(func(f protoreflect.FileDescriptor) bool {
		if !strings.HasPrefix(f.Path(), "google/protobuf/") {
			entries = append(entries, f.Path())
		}
		return true
	})
	sort.Strings(entries)
	for _, name := range entries {
		f, _ := s.contract.Files.FindFileByPath(name)
		visit(f)
	}
	plugin, err := (protogen.Options{}).New(&pluginpb.CodeGeneratorRequest{
		ProtoFile: set.File, FileToGenerate: entries, Parameter: proto.String("paths=source_relative"),
	})
	if err != nil {
		return fmt.Errorf("generate Go (set go_package in each application Proto): %w", err)
	}
	for _, f := range plugin.Files {
		if f.Generate {
			gengo.GenerateFile(plugin, f)
		}
	}
	response := plugin.Response()
	if response.GetError() != "" {
		return fmt.Errorf("generate Go: %s", response.GetError())
	}
	for _, f := range response.File {
		if !localProtoPath(f.GetName()) {
			return fmt.Errorf("generated Go path escapes output directory: %q", f.GetName())
		}
		path := filepath.Join(dir, filepath.FromSlash(f.GetName()))
		if err := ensureDir(filepath.Dir(path)); err != nil {
			return err
		}
		if err := atomicWrite(path, []byte(f.GetContent())); err != nil {
			return err
		}
	}
	return nil
}

func localProtoPath(name string) bool {
	return filepath.IsLocal(filepath.FromSlash(name)) && !strings.ContainsAny(name, "\\:") && path.Clean(name) == name
}

// LoadSourceContract attaches optional RPC transport rules to an already
// compiled schema. The manifest's descriptor path is replaced by this schema.
func LoadSourceContract(path string, s *Schema) (*Contract, error) {
	if s == nil {
		return nil, fmt.Errorf("compiled schema is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := ValidateJSON(data); err != nil {
		return nil, err
	}
	var m Manifest
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&m); err != nil {
		return nil, err
	}
	return Resolve(m, s.DescriptorSet())
}
