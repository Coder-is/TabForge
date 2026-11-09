package protocol

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/structpb"
)

const realtimeContract = "../examples/realtime/Generated/protocol/contract.json"

func realtimeExample(t *testing.T) (*Contract, *descriptorpb.FileDescriptorSet) {
	t.Helper()
	c, err := Load(realtimeContract)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(realtimeContract), "schema.pb"))
	if err != nil {
		t.Fatal(err)
	}
	set := new(descriptorpb.FileDescriptorSet)
	if err := proto.Unmarshal(data, set); err != nil {
		t.Fatal(err)
	}
	return c, set
}

// cloneManifest deep-copies channels so edits do not leak between cases.
func cloneManifest(m Manifest) Manifest {
	data, _ := json.Marshal(m)
	var out Manifest
	json.Unmarshal(data, &out)
	return out
}

func TestResolveRejectsInvalidChannels(t *testing.T) {
	cases := map[string]func(*Manifest){
		"duplicate channel": func(m *Manifest) { m.Channels = append(m.Channels, m.Channels[0]) },
		"bad path":          func(m *Manifest) { m.Channels[0].Path = "/ws/../x" },
		"path clash": func(m *Manifest) {
			m.Endpoints = []Endpoint{{ID: "x", RPC: "tabforge.game.Game.Login", Path: m.Channels[0].Path, Transport: "http_json", Auth: "none", TimeoutMS: 1}}
		},
		"auth":                     func(m *Manifest) { m.Channels[0].Auth = "cookie" },
		"heartbeat":                func(m *Manifest) { m.Channels[0].Heartbeat = 10 },
		"resume":                   func(m *Manifest) { m.Channels[0].Resume = -1 },
		"no messages":              func(m *Manifest) { m.Channels[0].Messages = nil },
		"duplicate id":             func(m *Manifest) { m.Channels[0].Messages[1].ID = m.Channels[0].Messages[0].ID },
		"zero id":                  func(m *Manifest) { m.Channels[0].Messages[0].ID = 0 },
		"large id":                 func(m *Manifest) { m.Channels[0].Messages[0].ID = MaxMessageID + 1 },
		"duplicate name":           func(m *Manifest) { m.Channels[0].Messages[1].Name = m.Channels[0].Messages[0].Name },
		"bad kind":                 func(m *Manifest) { m.Channels[0].Messages[0].Kind = "stream" },
		"request without rpc":      func(m *Manifest) { m.Channels[0].Messages[0].RPC = "" },
		"request with type":        func(m *Manifest) { m.Channels[0].Messages[0].Type = "tabforge.game.Move" },
		"request without timeout":  func(m *Manifest) { m.Channels[0].Messages[0].TimeoutMS = 0 },
		"volatile request":         func(m *Manifest) { m.Channels[0].Messages[0].Volatile = true },
		"push with rpc":            func(m *Manifest) { m.Channels[0].Messages[5].RPC = "tabforge.game.Game.Login" },
		"push of missing type":     func(m *Manifest) { m.Channels[0].Messages[5].Type = "tabforge.game.Missing" },
		"push of enum":             func(m *Manifest) { m.Channels[0].Messages[5].Type = "tabforge.game.Notice" },
		"push with timeout":        func(m *Manifest) { m.Channels[0].Messages[5].TimeoutMS = 5 },
		"rpc is a message":         func(m *Manifest) { m.Channels[0].Messages[0].RPC = "tabforge.game.Move" },
		"no endpoints or channels": func(m *Manifest) { m.Channels = nil },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			c, set := realtimeExample(t)
			m := cloneManifest(c.Manifest)
			edit(&m)
			if _, err := Resolve(m, set); err == nil {
				t.Fatal("invalid channel accepted")
			}
		})
	}
}

func TestChannelCodecRestrictions(t *testing.T) {
	c, set := realtimeExample(t)
	set = proto.Clone(set).(*descriptorpb.FileDescriptorSet)
	for _, f := range set.File {
		if f.GetPackage() != "tabforge.game" {
			continue
		}
		f.Dependency = append(f.Dependency, "google/protobuf/struct.proto")
		for _, m := range f.MessageType {
			if m.GetName() == "Move" {
				m.Field = append(m.Field, &descriptorpb.FieldDescriptorProto{Name: proto.String("extra"), Number: proto.Int32(9), Type: descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(), TypeName: proto.String(".google.protobuf.Struct"), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), JsonName: proto.String("extra")})
			}
		}
	}
	set.File = append([]*descriptorpb.FileDescriptorProto{structFile(t)}, set.File...)
	if _, err := Resolve(c.Manifest, set); err == nil || !strings.Contains(err.Error(), "google.protobuf.Struct") {
		t.Fatalf("Struct in a channel message: %v", err)
	}
}

func structFile(t *testing.T) *descriptorpb.FileDescriptorProto {
	return protodesc.ToFileDescriptorProto(structpb.File_google_protobuf_struct_proto)
}

func TestChannelGenerationIsStableAndComplete(t *testing.T) {
	c, _ := realtimeExample(t)
	dir := t.TempDir()
	if err := c.Generate(dir); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(filepath.Join(dir, "contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Fingerprint() != c.Fingerprint() {
		t.Fatal("bundle changed the schema hash")
	}
	ts, _ := c.TypeScript()
	again, _ := reloaded.TypeScript()
	if !bytes.Equal(ts, again) {
		t.Fatal("TypeScript output is not deterministic")
	}
	for _, want := range []string{`export interface ChannelTypes`, `"login": { request: T_tabforge__game__LoginRequest; response: T_tabforge__game__LoginReply }`, `"playerMoved": T_tabforge__game__PlayerMoved`, `export const codecSchema`, `"volatile": true`} {
		if !strings.Contains(string(ts), want) {
			t.Fatalf("types.ts lacks %s", want)
		}
	}
	runtime, _ := c.RuntimeJSON("")
	var decoded map[string]json.RawMessage
	json.Unmarshal(runtime, &decoded)
	if decoded["channels"] == nil || decoded["codecSchema"] == nil {
		t.Fatal("runtime.json lacks channel metadata")
	}
	if md := c.Markdown(); !strings.Contains(md, "## Realtime channels") || !strings.Contains(md, "| 202 | notice | push | tabforge.game.ServerNotice |") {
		t.Fatal("PROTOCOL.md lacks the channel table")
	}
	// HTTP-only contracts are unchanged by the channel feature.
	http, _ := example(t)
	runtime, _ = http.RuntimeJSON("")
	if bytes.Contains(runtime, []byte("channels")) || bytes.Contains(runtime, []byte("codecSchema")) {
		t.Fatal("HTTP-only runtime.json changed")
	}
}

func TestChannelCompatibility(t *testing.T) {
	old, set := realtimeExample(t)
	check := func(name string, breaking bool, edit func(*Manifest)) {
		t.Helper()
		m := cloneManifest(old.Manifest)
		edit(&m)
		next, err := Resolve(m, set)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if changes := BreakingChanges(old, next); (len(changes) != 0) != breaking {
			t.Fatalf("%s: breaking=%v, changes %v", name, breaking, changes)
		}
	}
	check("unchanged", false, func(m *Manifest) {})
	check("new push", false, func(m *Manifest) {
		m.Channels[0].Messages = append(m.Channels[0].Messages, ChannelMessage{ID: 300, Name: "extra", Kind: KindPush, Type: "tabforge.game.ChatMessage"})
	})
	check("new request", false, func(m *Manifest) {
		m.Channels[0].Messages = append(m.Channels[0].Messages, ChannelMessage{ID: 5, Name: "login2", Kind: KindRequest, RPC: "tabforge.game.Game.Login", TimeoutMS: 1})
	})
	check("longer timeout", false, func(m *Manifest) { m.Channels[0].Messages[0].TimeoutMS *= 2 })
	check("heartbeat change", false, func(m *Manifest) { m.Channels[0].Heartbeat = 2000 })
	check("removed message", true, func(m *Manifest) { m.Channels[0].Messages = m.Channels[0].Messages[1:] })
	check("renumbered", true, func(m *Manifest) { m.Channels[0].Messages[0].ID = 99 })
	check("id reused", true, func(m *Manifest) {
		m.Channels[0].Messages[0].Name = "login2"
	})
	check("volatile changed", true, func(m *Manifest) { m.Channels[0].Messages[4].Volatile = false })
	check("path", true, func(m *Manifest) { m.Channels[0].Path = "/ws/other" })
	check("shorter timeout", true, func(m *Manifest) { m.Channels[0].Messages[0].TimeoutMS = 1 })
	check("removed channel", true, func(m *Manifest) {
		m.Channels[0].ID = "renamed"
	})
}
