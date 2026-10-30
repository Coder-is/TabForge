package httptransport

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Coder-is/TabForge/protocol"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

// olderContract derives an older release of the example contract. edit
// changes the descriptor set and manifest the way the old release differed.
func olderContract(t *testing.T, version string, edit func(*descriptorpb.FileDescriptorProto, *protocol.Manifest)) *protocol.Contract {
	t.Helper()
	current, err := protocol.Load("../../examples/protocol/contract.json")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("../../examples/protocol/schema.pb")
	if err != nil {
		t.Fatal(err)
	}
	var set descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(data, &set); err != nil {
		t.Fatal(err)
	}
	manifest := current.Manifest
	manifest.Version = version
	manifest.Endpoints = append([]protocol.Endpoint(nil), manifest.Endpoints...)
	for i := range manifest.Endpoints {
		manifest.Endpoints[i].Events = append([]protocol.Event(nil), manifest.Endpoints[i].Events...)
	}
	var file *descriptorpb.FileDescriptorProto
	for _, f := range set.File {
		if f.GetPackage() == "tabforge.example" {
			file = f
		}
	}
	if file == nil {
		t.Fatal("example descriptor not found")
	}
	edit(file, &manifest)
	old, err := protocol.Resolve(manifest, &set)
	if err != nil {
		t.Fatal(err)
	}
	return old
}

func removeField(t *testing.T, file *descriptorpb.FileDescriptorProto, message, field string) {
	t.Helper()
	for _, m := range file.MessageType {
		if m.GetName() != message {
			continue
		}
		for i, f := range m.Field {
			if f.GetName() == field {
				m.Field = append(m.Field[:i], m.Field[i+1:]...)
				return
			}
		}
	}
	t.Fatalf("%s.%s not found", message, field)
}

// The 0.9.0 release had no ChatResponse.usage; 1.0.0 added it.
func withoutUsage(t *testing.T) *protocol.Contract {
	return olderContract(t, "0.9.0", func(file *descriptorpb.FileDescriptorProto, _ *protocol.Manifest) {
		removeField(t, file, "ChatResponse", "usage")
	})
}

func previousRequest(s *Server, path, version, hash, data string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(data))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Protocol-Version", version)
	if hash != "" {
		r.Header.Set("X-Protocol-Schema", hash)
	}
	r.Header.Set("X-Request-ID", "old-client")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestAcceptPreviousRegistration(t *testing.T) {
	s := fixture(t)
	old := withoutUsage(t)
	if len(protocol.BreakingChanges(old, s.Contract)) == 0 {
		t.Fatal("strict profile should reject the response addition without transcoding")
	}
	if err := s.AcceptPrevious(old); err != nil {
		t.Fatalf("compatible previous contract refused: %v", err)
	}
	if err := s.AcceptPrevious(withoutUsage(t)); err == nil {
		t.Fatal("duplicate previous contract accepted")
	}
	if err := s.AcceptPrevious(s.Contract); err == nil {
		t.Fatal("current contract accepted as previous")
	}
	if err := s.AcceptPrevious(nil); err == nil {
		t.Fatal("nil contract accepted")
	}
	for name, edit := range map[string]func(*descriptorpb.FileDescriptorProto, *protocol.Manifest){
		// Old clients would receive an event they cannot decode.
		"new event": func(file *descriptorpb.FileDescriptorProto, m *protocol.Manifest) {
			removeField(t, file, "ChatEvent", "usage")
			events := m.Endpoints[1].Events[:0]
			for _, e := range m.Endpoints[1].Events {
				if e.Name != "usage" {
					events = append(events, e)
				}
			}
			m.Endpoints[1].Events = events
		},
		// The current contract removed a field old clients send or read.
		"removed field": func(file *descriptorpb.FileDescriptorProto, _ *protocol.Manifest) {
			for _, msg := range file.MessageType {
				if msg.GetName() == "ChatResponse" {
					msg.Field = append(msg.Field, &descriptorpb.FieldDescriptorProto{Name: proto.String("legacy"), JsonName: proto.String("legacy"), Number: proto.Int32(9), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum()})
				}
			}
		},
		"moved endpoint": func(_ *descriptorpb.FileDescriptorProto, m *protocol.Manifest) {
			m.Endpoints[0].Path = "/v0/chat/complete"
		},
		"longer deadline": func(_ *descriptorpb.FileDescriptorProto, m *protocol.Manifest) {
			m.Endpoints[0].TimeoutMS++
		},
	} {
		if err := fixture(t).AcceptPrevious(olderContract(t, "0.8.0", edit)); err == nil {
			t.Fatalf("%s: breaking previous contract accepted", name)
		}
	}
}

func TestAcceptPreviousUnaryTranscodes(t *testing.T) {
	s := fixture(t)
	old := withoutUsage(t)
	oldHash := old.Fingerprint()
	if err := s.AcceptPrevious(old); err != nil {
		t.Fatal(err)
	}
	var observed Result
	s.Observe = func(r Result) { observed = r }
	e, _ := s.Contract.Endpoint("chatComplete")
	if err := s.HandleUnary(e.ID, func(ctx context.Context, req proto.Message) (proto.Message, error) {
		// Handlers always see the current schema.
		if req.ProtoReflect().Descriptor() != e.Input {
			t.Error("handler received a previous-contract message")
		}
		f := req.ProtoReflect().Descriptor().Fields().ByName("prompt")
		switch req.ProtoReflect().Get(f).String() {
		case "fail":
			return nil, &Error{Code: "bad_request", Message: "rejected"}
		case "invalid":
			return message(t, s.Contract.Endpoints[1].Output, `{}`), nil
		}
		return message(t, e.Output, `{"text":"你好","usage":{"outputTokens":"18446744073709551615"}}`), nil
	}); err != nil {
		t.Fatal(err)
	}
	decode := func(w *httptest.ResponseRecorder) envelope {
		t.Helper()
		var body envelope
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("%v: %s", err, w.Body.String())
		}
		return body
	}

	w := previousRequest(s, e.Path, "0.9.0", oldHash, `{"prompt":"hi","conversationId":"18446744073709551615"}`)
	body := decode(w)
	if w.Code != 200 || body.ProtocolVersion != "0.9.0" || body.SchemaHash != oldHash || string(body.Data) != `{"text":"你好"}` {
		t.Fatalf("old client reply: %d %s", w.Code, w.Body.String())
	}
	if w.Header().Get("X-Protocol-Version") != "0.9.0" || w.Header().Get("X-Protocol-Schema") != oldHash || observed.ProtocolVersion != "0.9.0" {
		t.Fatalf("old identity headers/observation: %v %+v", w.Header(), observed)
	}
	// Old fields are validated with the old schema: usage did not exist yet.
	if w := previousRequest(s, e.Path, "0.9.0", oldHash, `{"usage":{}}`); w.Code != 400 {
		t.Fatalf("unknown old field accepted: %d", w.Code)
	}
	w = previousRequest(s, e.Path, "0.9.0", oldHash, `{"prompt":"fail"}`)
	if body := decode(w); w.Code != 400 || body.Error == nil || body.Error.Code != "bad_request" || body.ProtocolVersion != "0.9.0" || body.SchemaHash != oldHash {
		t.Fatalf("old client error: %d %s", w.Code, w.Body.String())
	}
	w = previousRequest(s, e.Path, "0.9.0", oldHash, `{"prompt":"invalid"}`)
	if body := decode(w); w.Code != 500 || body.Error == nil || body.ProtocolVersion != "0.9.0" {
		t.Fatalf("invalid handler output: %d %s", w.Code, w.Body.String())
	}
	// Transcoded output still honors the response limit.
	s.MaxResponseBytes = 100
	if w := previousRequest(s, e.Path, "0.9.0", oldHash, `{}`); w.Code != 500 {
		t.Fatalf("response limit not applied: %d %s", w.Code, w.Body.String())
	}
	s.MaxResponseBytes = 0

	// Current clients are unaffected and receive the new field.
	w = request(s, e.Path, `{}`, nil)
	if body := decode(w); w.Code != 200 || body.SchemaHash != s.schemaHash || !strings.Contains(string(body.Data), "usage") {
		t.Fatalf("current client: %d %s", w.Code, w.Body.String())
	}
	for _, tc := range []struct{ version, hash, code string }{
		{"0.8.0", oldHash, "version_mismatch"},
		{"0.9.0", s.schemaHash, "schema_mismatch"},
		{"1.0.0", oldHash, "schema_mismatch"},
		{"0.9.0", "", "schema_mismatch"},
	} {
		w := previousRequest(s, e.Path, tc.version, tc.hash, `{}`)
		if body := decode(w); w.Code != 409 || body.Error.Code != tc.code || body.ProtocolVersion != "1.0.0" {
			t.Fatalf("%+v: %d %s", tc, w.Code, w.Body.String())
		}
	}
	s.RequireSchemaHash = false
	if w := previousRequest(s, e.Path, "0.9.0", "", `{}`); w.Code != 200 || decode(w).SchemaHash != oldHash {
		t.Fatalf("hashless old client: %d %s", w.Code, w.Body.String())
	}
}

func TestAcceptPreviousStreamTranscodes(t *testing.T) {
	s := fixture(t)
	old := withoutUsage(t)
	if err := s.AcceptPrevious(old); err != nil {
		t.Fatal(err)
	}
	e, _ := s.Contract.Endpoint("chatStream")
	if err := s.HandleStream(e.ID, func(ctx context.Context, req proto.Message, emit func(proto.Message) error) error {
		if err := emit(message(t, e.Output, `{"delta":{"text":"中文"}}`)); err != nil {
			return err
		}
		if err := emit(message(t, e.Output, `{"usage":{"inputTokens":"3"}}`)); err != nil {
			return err
		}
		return emit(message(t, e.Output, `{"completed":{"response":{"text":"中文","usage":{"outputTokens":"7"}}}}`))
	}); err != nil {
		t.Fatal(err)
	}
	w := previousRequest(s, e.Path, "0.9.0", old.Fingerprint(), `{"prompt":"hi"}`)
	body := w.Body.String()
	if w.Code != 200 || strings.Count(body, `"protocolVersion":"0.9.0","schemaHash":"`+old.Fingerprint()+`"`) != 3 {
		t.Fatalf("old identity not stamped on every frame: %s", body)
	}
	if !strings.Contains(body, "id: 3\nevent: completed\n") || !strings.Contains(body, `"payload":{"completed":{"response":{"text":"中文"}}}`) || strings.Contains(body, "outputTokens") || strings.Contains(body, "protocol.error") {
		t.Fatalf("stream not transcoded: %s", body)
	}
	// Event usage (a message type unchanged since 0.9.0) still passes through.
	if !strings.Contains(body, `"payload":{"usage":{"inputTokens":"3"}}`) {
		t.Fatalf("usage event lost: %s", body)
	}
}

// An old generated TypeScript bundle, with its strict validators, talks to the
// upgraded Go server.
func TestTypeScriptPreviousClientAgainstGoServer(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js 24+ required for cross-language integration")
	}
	version, err := exec.Command(node, "--version").Output()
	if err != nil {
		t.Fatal(err)
	}
	if major, _ := strconv.Atoi(strings.Split(strings.TrimPrefix(string(version), "v"), ".")[0]); major < 24 {
		t.Skip("Node.js 24+ required for native TypeScript integration")
	}
	runtime, err := filepath.Abs("../../sdk/typescript/runtime.ts")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(runtime); err != nil {
		t.Skip("TypeScript runtime source not available")
	}
	s := fixture(t)
	old := withoutUsage(t)
	if err := s.AcceptPrevious(old); err != nil {
		t.Fatal(err)
	}
	u, _ := s.Contract.Endpoint("chatComplete")
	e, _ := s.Contract.Endpoint("chatStream")
	if err := s.HandleUnary(u.ID, func(ctx context.Context, req proto.Message) (proto.Message, error) {
		if req.ProtoReflect().Get(req.ProtoReflect().Descriptor().Fields().ByName("prompt")).String() == "fail" {
			return nil, &Error{Code: "bad_request", Message: "rejected"}
		}
		return message(t, u.Output, `{"text":"你好😀","usage":{"outputTokens":"18446744073709551615"}}`), nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.HandleStream(e.ID, func(ctx context.Context, req proto.Message, emit func(proto.Message) error) error {
		if err := emit(message(t, e.Output, `{"delta":{"text":"你好😀"}}`)); err != nil {
			return err
		}
		return emit(message(t, e.Output, `{"completed":{"response":{"text":"你好😀","usage":{"inputTokens":"1"}}}}`))
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(s)
	defer server.Close()
	bundle := t.TempDir()
	if err := old.Generate(bundle); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "package.json"), []byte(`{"type":"module"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "previous.mjs")
	source := `import assert from "node:assert/strict";
import { pathToFileURL } from "node:url";
const { ProtocolClient, FetchTransport } = await import(pathToFileURL(process.argv[3]).href);
const { operations, protocolVersion, schemaHash, wireSchema } = await import(pathToFileURL(process.argv[4]).href);
assert.equal(protocolVersion, "0.9.0");
const client = new ProtocolClient(operations, new FetchTransport(process.argv[2], protocolVersion, globalThis.fetch, { schemaHash, wireSchema }));
const reply = await client.call("chatComplete", { prompt: "hi" });
assert.equal(JSON.stringify(reply), JSON.stringify({ text: "你好😀" }));
await assert.rejects(client.call("chatComplete", { prompt: "fail" }), (error) => error.code === "bad_request");
const events = [];
for await (const event of client.stream("chatStream", { prompt: "hi" })) events.push(event);
assert.equal(events[0].payload.delta.text, "你好😀");
assert.equal(events.at(-1).name, "completed");
assert.equal(JSON.stringify(events.at(-1).payload), JSON.stringify({ completed: { response: { text: "你好😀" } } }));
console.log("previous TypeScript client passed");
`
	if err := os.WriteFile(script, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(node, script, server.URL, runtime, filepath.Join(bundle, "types.ts")).CombinedOutput(); err != nil {
		t.Fatalf("previous TypeScript client: %v\n%s", err, output)
	}
}
