package protocol

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
)

func TestPureDataSchema(t *testing.T) {
	dir := t.TempDir()
	source := `syntax="proto3"; package test;
option go_package="example.com/project/generated;generated";
import "google/protobuf/timestamp.proto";
message Unused { string name=1; repeated Unused children=2; enum State { IDLE=0; ACTIVE=1; } State state=3; }
message Data { uint64 id=1; optional int32 level=2; oneof effect { string text=3; int32 power=4; } map<string,Unused> objects=5; google.protobuf.Timestamp time=6; }
enum UnusedEnum { NONE=0; VALUE=1; }`
	if err := os.WriteFile(filepath.Join(dir, "data.proto"), []byte(source), 0644); err != nil {
		t.Fatal(err)
	}
	s, err := CompileSchema(context.Background(), []string{dir}, []string{"data.proto"})
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "out")
	if err := s.Generate(output, true); err != nil {
		t.Fatal(err)
	}
	ts, err := os.ReadFile(filepath.Join(output, "types.ts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{`"test.Unused"`, "T_test__UnusedEnum", "MessageTypes", "wireSchema"} {
		if !strings.Contains(string(ts), required) {
			t.Errorf("missing %s", required)
		}
	}
	if strings.Contains(string(ts), "ProtocolTypes") || strings.Contains(string(ts), "operations =") {
		t.Fatal("pure schema emitted network contract")
	}
	for _, name := range []string{"data.ts", "schema.ts", "go/data.pb.go", "SCHEMA.md", "bundle.json"} {
		if _, err := os.Stat(filepath.Join(output, name)); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := LoadSchema(filepath.Join(output, "schema.pb"))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Fingerprint() != s.Fingerprint() {
		t.Fatal("fingerprint changed after distribution")
	}
	message, err := loaded.DecodeJSON("test.Data", []byte(`{"id":"18446744073709551615","level":0,"text":"中文😀","time":"2026-10-04T00:00:00Z","objects":{"a":{"name":"nested"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	fields := message.ProtoReflect().Descriptor().Fields()
	if !message.ProtoReflect().Has(fields.ByName("level")) {
		t.Fatal("lost optional zero presence")
	}
	if got := message.ProtoReflect().Get(fields.ByName("id")).Uint(); got != ^uint64(0) {
		t.Fatalf("lost uint64 precision: %d", got)
	}
	binary, err := proto.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	roundtrip, err := loaded.DecodeBinary("test.Data", binary)
	if err != nil || !proto.Equal(message, roundtrip) {
		t.Fatalf("binary roundtrip: %v", err)
	}
	for _, json := range []string{`{"id": "18446744073709551616"}`, `{"text":"x","power":0}`, `{"unknown":1}`, `{"level":0,"level":1}`} {
		if _, err := loaded.DecodeJSON("test.Data", []byte(json)); err == nil {
			t.Fatalf("accepted %s", json)
		}
	}
	if _, err := loaded.NewMessage("test.Missing"); err == nil {
		t.Fatal("unknown type accepted")
	}
}

func TestProtoSourceDiagnosticsAndCancellation(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.proto"), []byte("syntax=\"proto3\";\nmessage Broken { int32 id = ; }"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := CompileSchema(context.Background(), []string{dir}, []string{"bad.proto"}); err == nil || !strings.Contains(err.Error(), "bad.proto:2:") {
		t.Fatalf("missing source location: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CompileSchema(ctx, []string{dir}, []string{"bad.proto"}); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestProtoImportsCannotTraverseOutsideRoot(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "proto")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "outside.proto"), []byte(`syntax="proto3"; message Outside {}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data.proto"), []byte(`syntax="proto3"; import "../outside.proto"; message Data { Outside value=1; }`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := CompileSchema(context.Background(), []string{dir}, []string{"data.proto"}); err == nil || !strings.Contains(err.Error(), "relative slash-separated") {
		t.Fatalf("traversal import accepted: %v", err)
	}
	if _, err := LoadSourceContract("unused", nil); err == nil {
		t.Fatal("nil schema accepted")
	}
}
