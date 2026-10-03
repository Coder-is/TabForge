package protocol

import (
	"bytes"
	"io/ioutil"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

func example(t *testing.T) (*Contract, *descriptorpb.FileDescriptorSet) {
	t.Helper()
	c, err := Load("../examples/protocol/contract.json")
	if err != nil {
		t.Fatal(err)
	}
	data, err := ioutil.ReadFile("../examples/protocol/schema.pb")
	if err != nil {
		t.Fatal(err)
	}
	set := new(descriptorpb.FileDescriptorSet)
	if err := proto.Unmarshal(data, set); err != nil {
		t.Fatal(err)
	}
	return c, set
}

func TestResolveRejectsInvalidContracts(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Manifest)
	}{
		{"schema-version", func(m *Manifest) { m.SchemaVersion = "2" }},
		{"duplicate-id", func(m *Manifest) { m.Endpoints[1].ID = m.Endpoints[0].ID }},
		{"duplicate-route", func(m *Manifest) { m.Endpoints[1].Path = m.Endpoints[0].Path }},
		{"path-traversal", func(m *Manifest) { m.Endpoints[0].Path = "/.." }},
		{"timeout", func(m *Manifest) { m.Endpoints[0].TimeoutMS = 0 }},
		{"unknown-auth", func(m *Manifest) { m.Endpoints[0].Auth = "maybe" }},
		{"missing-rpc", func(m *Manifest) { m.Endpoints[0].RPC = "missing.Service.Call" }},
		{"not-method", func(m *Manifest) { m.Endpoints[0].RPC = "tabforge.example.ChatRequest" }},
		{"stream-on-unary", func(m *Manifest) { m.Endpoints[0].Transport = "http_sse" }},
		{"unary-on-stream", func(m *Manifest) { m.Endpoints[1].Transport = "http_json" }},
		{"unimplemented-transport", func(m *Manifest) { m.Endpoints[0].Transport = "websocket" }},
		{"missing-event", func(m *Manifest) { m.Endpoints[1].Events = m.Endpoints[1].Events[:1] }},
		{"invalid-event-field", func(m *Manifest) { m.Endpoints[1].Events[0].Field = "missing" }},
		{"reserved-event", func(m *Manifest) { m.Endpoints[1].Events[0].Name = "protocol.error" }},
		{"duplicate-event", func(m *Manifest) { m.Endpoints[1].Events[1].Name = m.Endpoints[1].Events[0].Name }},
		{"no-terminal", func(m *Manifest) {
			for i := range m.Endpoints[1].Events {
				m.Endpoints[1].Events[i].Terminal = false
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, set := example(t)
			tc.edit(&c.Manifest)
			if _, err := Resolve(c.Manifest, set); err == nil {
				t.Fatal("invalid contract accepted")
			}
		})
	}
}

func TestBundleReloadAndDeterministicTypes(t *testing.T) {
	c, _ := example(t)
	dir := t.TempDir()
	if err := c.Generate(dir); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(filepath.Join(dir, "contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	before, err := c.TypeScript()
	if err != nil {
		t.Fatal(err)
	}
	after, err := reloaded.TypeScript()
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("bundle changed types: %v", err)
	}
	if !strings.Contains(string(before), `"conversationId"?: string`) || !strings.Contains(string(before), `"toolDelta"?: never`) {
		t.Fatal("ProtoJSON precision or oneof contract lost")
	}
	if !strings.Contains(c.Markdown(), "ToolCallDelta") || !strings.Contains(c.Markdown(), "Terminal") {
		t.Fatal("stream contract omitted from documentation")
	}
}

func TestStrictManifestJSON(t *testing.T) {
	for _, data := range []string{`{"schema_version":"1","unknown":true}`, `{} {}`} {
		path := filepath.Join(t.TempDir(), "bad.json")
		if err := ioutil.WriteFile(path, []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatal("malformed manifest accepted")
		}
	}
}
