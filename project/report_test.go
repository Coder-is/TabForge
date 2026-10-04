package project

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCheckAndStructuredSourceDiagnostics(t *testing.T) {
	root := copyFixture(t)
	p, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	r, err := p.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Data) != 2 {
		t.Fatalf("missing data root associations: %+v", r.Data)
	}
	if _, err := os.Stat(r.Output); !os.IsNotExist(err) {
		t.Fatal("check published output")
	}
	if _, err := p.Export(context.Background()); err != nil {
		t.Fatal(err)
	}
	old := snapshot(t, r.Output)
	bad := filepath.Join(root, "Tables", "Basic", "ActorsExtra.csv")
	data, err := os.ReadFile(bad)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte(strings.Replace(string(data), "3,合并", "1,合并", 1)), 0644); err != nil {
		t.Fatal(err)
	}
	_, err = p.Check(context.Background())
	if err == nil {
		t.Fatal("duplicate accepted")
	}
	d := p.Diagnose(err)
	if len(d) != 1 || d[0].Code != "DuplicateValueInMakingIndex" || d[0].Path != bad || d[0].Line != 2 || d[0].Cell != "A2" {
		t.Fatalf("missing source identity: %+v", d)
	}
	if !reflect.DeepEqual(old, snapshot(t, r.Output)) {
		t.Fatal("failed check modified output")
	}
	if err := os.WriteFile(filepath.Join(root, "Protocols", "bad.proto"), []byte("syntax = \"proto3\";\nmessage Broken { string name = ; }\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err = p.Check(context.Background())
	if err == nil {
		t.Fatal("bad Proto accepted")
	}
	d = p.Diagnose(err)
	if len(d) != 1 || d[0].Code != "proto_compile" || d[0].Line != 2 || d[0].Path != filepath.Join(root, "Protocols", "bad.proto") {
		t.Fatalf("Proto location: %+v", d)
	}
}

func TestReportCannotOverwriteSymlink(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "source.json")
	if err := os.WriteFile(outside, []byte("source"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, ReportName)); err != nil {
		t.Skip(err)
	}
	if err := WriteReport(root, RunReport{}); err == nil {
		t.Fatal("report followed a symlink")
	}
	data, err := os.ReadFile(outside)
	if err != nil || string(data) != "source" {
		t.Fatal("report changed source")
	}
}
