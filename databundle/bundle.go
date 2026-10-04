// Package databundle loads exported data without source tables or a compiler.
// Open validates the whole manifest before returning an immutable snapshot.
package databundle

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/Coder-is/TabForge/protocol"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

const ManifestName = "data_manifest.json"
const Format = "tabforge.data.v1"

type File struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}
type Entry struct {
	File
	Message  string `json:"message"`
	Encoding string `json:"encoding"` // protojson or protobuf
}
type Manifest struct {
	Format     string  `json:"format"`
	SchemaHash string  `json:"schemaHash"`
	Descriptor File    `json:"descriptor"`
	WireSchema File    `json:"wireSchema"`
	Data       []Entry `json:"data"`
}

// Digest identifies the exact bytes of an exported file. It detects incomplete
// or mixed bundles; authenticity requires a trusted distributor or pinned hash.
func Digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

type Bundle struct {
	manifest Manifest
	schema   *protocol.Schema
	data     map[string]proto.Message
}

// Open loads a Generated directory. expectedHash may be empty or pin a schema.
func Open(dir, expectedHash string) (*Bundle, error) {
	return OpenFS(os.DirFS(dir), ".", expectedHash)
}

// OpenFS supports embed.FS and other read-only filesystems. dir uses slash paths.
func OpenFS(fsys fs.FS, dir, expectedHash string) (*Bundle, error) {
	if !fs.ValidPath(dir) {
		return nil, fmt.Errorf("invalid bundle directory %q", dir)
	}
	read := func(name string) ([]byte, error) {
		if !local(name) {
			return nil, fmt.Errorf("invalid bundle path %q", name)
		}
		full := path.Join(dir, name)
		// DirFS follows symlinks; inspect each entry before reading it.
		parent := "."
		for _, part := range strings.Split(full, "/") {
			entries, err := fs.ReadDir(fsys, parent)
			if err != nil {
				return nil, err
			}
			for _, entry := range entries {
				if entry.Name() == part && entry.Type()&fs.ModeSymlink != 0 {
					return nil, fmt.Errorf("symlink bundle path %q", name)
				}
			}
			parent = path.Join(parent, part)
		}
		info, err := fs.Stat(fsys, full)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("bundle file is not regular: %s", name)
		}
		return fs.ReadFile(fsys, full)
	}
	raw, err := read(ManifestName)
	if err != nil {
		return nil, fmt.Errorf("read %s (regenerate with TabForge v3): %w", ManifestName, err)
	}
	if err := protocol.ValidateJSON(raw); err != nil {
		return nil, err
	}
	var m Manifest
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&m); err != nil {
		return nil, err
	}
	if m.Format != Format || !hash(m.SchemaHash) || m.Data == nil || (expectedHash != "" && expectedHash != m.SchemaHash) {
		return nil, fmt.Errorf("invalid data manifest or schema hash mismatch")
	}
	seen := map[string]bool{ManifestName: true}
	checked := func(f File) ([]byte, error) {
		key := strings.ToLower(f.Path)
		if !local(f.Path) || seen[key] || !hash(f.SHA256) {
			return nil, fmt.Errorf("invalid or duplicate manifest file %q", f.Path)
		}
		seen[key] = true
		data, err := read(f.Path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.Path, err)
		}
		if Digest(data) != f.SHA256 {
			return nil, fmt.Errorf("checksum mismatch: %s", f.Path)
		}
		return data, nil
	}
	pb, err := checked(m.Descriptor)
	if err != nil {
		return nil, err
	}
	set := new(descriptorpb.FileDescriptorSet)
	if err := proto.Unmarshal(pb, set); err != nil {
		return nil, err
	}
	s, err := protocol.NewSchema(set)
	if err != nil {
		return nil, err
	}
	if s.Fingerprint() != m.SchemaHash {
		return nil, fmt.Errorf("descriptor schema hash mismatch")
	}
	wire, err := checked(m.WireSchema)
	if err != nil {
		return nil, err
	}
	if err := protocol.ValidateJSON(wire); err != nil {
		return nil, err
	}
	var actual protocol.WireSchema
	d = json.NewDecoder(bytes.NewReader(wire))
	d.DisallowUnknownFields()
	if err := d.Decode(&actual); err != nil {
		return nil, err
	}
	a, _ := json.Marshal(actual)
	b, _ := json.Marshal(s.WireSchema())
	if !bytes.Equal(a, b) {
		return nil, fmt.Errorf("wire schema does not match descriptor")
	}
	bundle := &Bundle{manifest: m, schema: s, data: map[string]proto.Message{}}
	for _, entry := range m.Data {
		data, err := checked(entry.File)
		if err != nil {
			return nil, err
		}
		var msg proto.Message
		switch entry.Encoding {
		case "protojson":
			msg, err = s.DecodeJSON(entry.Message, data)
		case "protobuf":
			msg, err = s.DecodeBinary(entry.Message, data)
		default:
			err = fmt.Errorf("unsupported encoding %q", entry.Encoding)
		}
		if err != nil {
			return nil, fmt.Errorf("%s (%s): %w", entry.Path, entry.Message, err)
		}
		bundle.data[entry.Path] = msg
	}
	return bundle, nil
}

func local(name string) bool {
	return fs.ValidPath(name) && name != "." && !strings.ContainsAny(name, "\\:")
}
func hash(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && value == strings.ToLower(value)
}
func (b *Bundle) SchemaHash() string { return b.manifest.SchemaHash }
func (b *Bundle) Entries() []Entry   { return append([]Entry(nil), b.manifest.Data...) }

// DecodeJSON validates any message from the loaded schema, including types that
// are not referenced by a table or RPC.
func (b *Bundle) DecodeJSON(message string, data []byte) (proto.Message, error) {
	return b.schema.DecodeJSON(message, data)
}

func (b *Bundle) DecodeBinary(message string, data []byte) (proto.Message, error) {
	return b.schema.DecodeBinary(message, data)
}

// Read returns an independent message; a caller cannot mutate the snapshot.
func (b *Bundle) Read(name string) (proto.Message, error) {
	m, ok := b.data[name]
	if !ok {
		return nil, fmt.Errorf("data file not declared: %s", name)
	}
	return proto.Clone(m), nil
}

// ReadInto supports generated Go messages and rejects stale descriptors. dst is
// replaced only after all checks and decoding succeed.
func (b *Bundle) ReadInto(name string, dst proto.Message) error {
	if dst == nil || !dst.ProtoReflect().IsValid() {
		return fmt.Errorf("destination message is nil")
	}
	m, ok := b.data[name]
	if !ok {
		return fmt.Errorf("data file not declared: %s", name)
	}
	if m.ProtoReflect().Descriptor().FullName() != dst.ProtoReflect().Descriptor().FullName() {
		return fmt.Errorf("destination message type mismatch")
	}
	files := map[string]*descriptorpb.FileDescriptorProto{}
	for _, f := range b.schema.DescriptorSet().File {
		f.SourceCodeInfo = nil
		files[f.GetName()] = f
	}
	seen := map[string]bool{}
	var check func(protoreflect.FileDescriptor) error
	check = func(f protoreflect.FileDescriptor) error {
		if seen[f.Path()] {
			return nil
		}
		seen[f.Path()] = true
		p := protodesc.ToFileDescriptorProto(f)
		p.SourceCodeInfo = nil
		if !proto.Equal(p, files[f.Path()]) {
			return fmt.Errorf("destination generated schema mismatch: %s", f.Path())
		}
		for i := 0; i < f.Imports().Len(); i++ {
			if err := check(f.Imports().Get(i).FileDescriptor); err != nil {
				return err
			}
		}
		return nil
	}
	if err := check(dst.ProtoReflect().Descriptor().ParentFile()); err != nil {
		return err
	}
	data, err := proto.Marshal(m)
	if err != nil {
		return err
	}
	next := dst.ProtoReflect().New().Interface()
	if err := proto.Unmarshal(data, next); err != nil {
		return err
	}
	proto.Reset(dst)
	proto.Merge(dst, next)
	return nil
}

// Store swaps validated snapshots for concurrent readers. A failed reload leaves
// the current snapshot intact. Each request should retain one Snapshot pointer.
type Store struct{ current atomic.Pointer[Bundle] }

func (s *Store) Snapshot() *Bundle { return s.current.Load() }
func (s *Store) Reload(dir, expectedHash string) error {
	b, err := Open(dir, expectedHash)
	if err != nil {
		return err
	}
	s.current.Store(b)
	return nil
}

// WriteManifest writes the portable runtime index after all data is generated.
// Entries may reference ProtoJSON or Protobuf; legacy V3 .bin is excluded.
func WriteManifest(dir string, schemaHash string, entries []Entry) error {
	entries = append([]Entry(nil), entries...)
	file := func(name string) (File, error) {
		if !local(name) {
			return File{}, fmt.Errorf("invalid data path %q", name)
		}
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
		return File{Path: name, SHA256: Digest(data)}, err
	}
	descriptor, err := file("schema/schema.pb")
	if err != nil {
		return err
	}
	wire, err := file("schema/wire_schema.json")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	for i := range entries {
		entries[i].File, err = file(entries[i].Path)
		if err != nil {
			return err
		}
	}
	if entries == nil {
		entries = []Entry{}
	}
	m := Manifest{Format: Format, SchemaHash: schemaHash, Descriptor: descriptor, WireSchema: wire, Data: entries}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, ManifestName), append(data, '\n'), 0644)
}
