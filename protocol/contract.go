// Package protocol validates transport contracts against existing Protobuf types.
// It is independent of the Excel/CSV compiler.
package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"path/filepath"
	"regexp"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

const SchemaVersion = "1"

type Manifest struct {
	SchemaVersion string     `json:"schema_version"`
	Name          string     `json:"name"`
	Version       string     `json:"version"`
	Descriptor    string     `json:"descriptor"`
	Endpoints     []Endpoint `json:"endpoints"`
}

type Endpoint struct {
	ID        string  `json:"id"`
	RPC       string  `json:"rpc"`
	Path      string  `json:"path"`
	Transport string  `json:"transport"` // http_json or http_sse; POST in v1.
	Auth      string  `json:"auth"`      // none or bearer; enforcement is application-owned.
	TimeoutMS int     `json:"timeout_ms"`
	Events    []Event `json:"events,omitempty"`
}

// Event.Field is a message member of the streamed response's payload oneof.
type Event struct {
	Name     string `json:"name"`
	Field    string `json:"field"`
	Terminal bool   `json:"terminal"`
}

type ResolvedEndpoint struct {
	Endpoint
	Input  protoreflect.MessageDescriptor
	Output protoreflect.MessageDescriptor
}

type Contract struct {
	Manifest  Manifest
	Files     *protoregistry.Files
	Types     *protoregistry.Types
	Endpoints []ResolvedEndpoint
}

// Load resolves descriptor paths relative to the manifest, unlike table indexes.
func Load(path string) (*Contract, error) {
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&m); err != nil {
		return nil, fmt.Errorf("decode protocol manifest: %v", err)
	}
	if err := d.Decode(new(interface{})); err != io.EOF {
		return nil, fmt.Errorf("protocol manifest must contain one JSON object")
	}
	if m.Descriptor == "" {
		return nil, fmt.Errorf("descriptor is required")
	}
	descriptorPath := m.Descriptor
	if !filepath.IsAbs(descriptorPath) {
		descriptorPath = filepath.Join(filepath.Dir(path), descriptorPath)
	}
	data, err = ioutil.ReadFile(descriptorPath)
	if err != nil {
		return nil, fmt.Errorf("read descriptor: %v", err)
	}
	var set descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(data, &set); err != nil {
		return nil, fmt.Errorf("decode descriptor: %v", err)
	}
	return Resolve(m, &set)
}

var identifier = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)
var route = regexp.MustCompile(`^/[A-Za-z0-9_./-]+$`)
var eventName = regexp.MustCompile(`^[a-z][a-z0-9_.-]*$`)
var versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9.-]+)?$`)

func Resolve(m Manifest, set *descriptorpb.FileDescriptorSet) (*Contract, error) {
	if m.SchemaVersion != SchemaVersion || m.Name == "" || m.Version == "" || len(m.Endpoints) == 0 {
		return nil, fmt.Errorf("protocol requires schema_version=%q, name, version and endpoints", SchemaVersion)
	}
	if !versionPattern.MatchString(m.Version) {
		return nil, fmt.Errorf("version must use major.minor.patch with an optional prerelease suffix")
	}
	if set == nil {
		return nil, fmt.Errorf("descriptor set is required")
	}
	files, err := protodesc.NewFiles(set)
	if err != nil {
		return nil, fmt.Errorf("resolve descriptors (use protoc --include_imports): %v", err)
	}
	c := &Contract{Manifest: m, Files: files, Types: new(protoregistry.Types)}
	files.RangeFiles(func(f protoreflect.FileDescriptor) bool {
		err = c.register(f.Messages())
		return err == nil
	})
	if err != nil {
		return nil, err
	}
	ids, paths := map[string]bool{}, map[string]bool{}
	for _, e := range m.Endpoints {
		if !identifier.MatchString(e.ID) || ids[e.ID] {
			return nil, fmt.Errorf("invalid or duplicate endpoint id %q", e.ID)
		}
		if !route.MatchString(e.Path) || strings.Contains(e.Path, "//") || strings.Contains(e.Path, "/../") || strings.HasSuffix(e.Path, "/") || paths[e.Path] {
			return nil, fmt.Errorf("invalid or duplicate POST path %q", e.Path)
		}
		for _, segment := range strings.Split(e.Path, "/") {
			if segment == "." || segment == ".." {
				return nil, fmt.Errorf("invalid path segment in %q", e.Path)
			}
		}
		if e.Auth != "none" && e.Auth != "bearer" {
			return nil, fmt.Errorf("%s: auth must be none or bearer", e.ID)
		}
		if e.TimeoutMS <= 0 || e.TimeoutMS > 86400000 {
			return nil, fmt.Errorf("%s: timeout_ms must be between 1 and 86400000", e.ID)
		}
		ids[e.ID], paths[e.Path] = true, true
		desc, err := files.FindDescriptorByName(protoreflect.FullName(e.RPC))
		if err != nil {
			return nil, fmt.Errorf("%s: RPC %q: %v", e.ID, e.RPC, err)
		}
		rpc, ok := desc.(protoreflect.MethodDescriptor)
		if !ok || rpc.IsStreamingClient() {
			return nil, fmt.Errorf("%s: RPC must be unary or server streaming", e.ID)
		}
		switch e.Transport {
		case "http_json":
			if rpc.IsStreamingServer() || len(e.Events) != 0 {
				return nil, fmt.Errorf("%s: http_json requires a unary RPC and no events", e.ID)
			}
		case "http_sse":
			if !rpc.IsStreamingServer() {
				return nil, fmt.Errorf("%s: http_sse requires a server streaming RPC", e.ID)
			}
			if err := validateEvents(e, rpc.Output()); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("%s: unsupported transport %q (v1 supports http_json and http_sse)", e.ID, e.Transport)
		}
		c.Endpoints = append(c.Endpoints, ResolvedEndpoint{Endpoint: e, Input: rpc.Input(), Output: rpc.Output()})
	}
	return c, nil
}

func (c *Contract) register(messages protoreflect.MessageDescriptors) error {
	for i := 0; i < messages.Len(); i++ {
		m := messages.Get(i)
		if err := c.Types.RegisterMessage(dynamicpb.NewMessageType(m)); err != nil {
			return err
		}
		if err := c.register(m.Messages()); err != nil {
			return err
		}
	}
	return nil
}

func validateEvents(e Endpoint, output protoreflect.MessageDescriptor) error {
	if output.Oneofs().Len() != 1 || output.Fields().Len() != output.Oneofs().Get(0).Fields().Len() {
		return fmt.Errorf("%s: SSE response must contain exactly one payload oneof and no other fields", e.ID)
	}
	names, fields := map[string]bool{}, map[protoreflect.FieldNumber]bool{}
	terminal := false
	for _, ev := range e.Events {
		if !eventName.MatchString(ev.Name) || ev.Name == "protocol.error" || names[ev.Name] {
			return fmt.Errorf("%s: invalid, reserved or duplicate event %q", e.ID, ev.Name)
		}
		f := output.Fields().ByName(protoreflect.Name(ev.Field))
		if f == nil || f.ContainingOneof() == nil || f.Kind() != protoreflect.MessageKind || fields[f.Number()] {
			return fmt.Errorf("%s: event field %q must be a unique oneof message member", e.ID, ev.Field)
		}
		names[ev.Name], fields[f.Number()], terminal = true, true, terminal || ev.Terminal
	}
	if !terminal || len(fields) != output.Fields().Len() {
		return fmt.Errorf("%s: map every oneof member to an event and mark at least one terminal event", e.ID)
	}
	return nil
}

func (c *Contract) Endpoint(id string) (ResolvedEndpoint, bool) {
	for _, e := range c.Endpoints {
		if e.ID == id {
			return e, true
		}
	}
	return ResolvedEndpoint{}, false
}

// EventFor validates the stream payload and derives framing from the contract.
func (e ResolvedEndpoint) EventFor(message proto.Message) (Event, error) {
	if message == nil || !message.ProtoReflect().IsValid() || message.ProtoReflect().Descriptor().FullName() != e.Output.FullName() {
		return Event{}, fmt.Errorf("%s: expected response %s", e.ID, e.Output.FullName())
	}
	if e.Transport != "http_sse" {
		return Event{}, fmt.Errorf("%s: not a stream endpoint", e.ID)
	}
	oneof := message.ProtoReflect().Descriptor().Oneofs().ByName(e.Output.Oneofs().Get(0).Name())
	if oneof == nil {
		return Event{}, fmt.Errorf("%s: response schema does not match contract", e.ID)
	}
	field := message.ProtoReflect().WhichOneof(oneof)
	if field != nil {
		for _, event := range e.Events {
			if event.Field == string(field.Name()) {
				return event, nil
			}
		}
	}
	return Event{}, fmt.Errorf("%s: stream payload must select a mapped oneof member", e.ID)
}
