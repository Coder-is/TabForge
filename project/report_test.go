package project

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
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

func junction(t *testing.T, link, target string) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("directory junctions are Windows-only")
	}
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Skipf("mklink /J: %v %s", err, out)
	}
}

// Go reports junctions as ModeIrregular, not ModeSymlink; every symlink guard
// must reject them too.
func TestJunctionsAreRejectedLikeSymlinks(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "keep.txt"), []byte("outside"), 0644); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, outside)

	root := copyFixture(t)
	junction(t, filepath.Join(root, "Linked"), outside)
	p, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	p.Config.Output = "Linked/Generated"
	if _, err := p.Export(context.Background()); err == nil || !strings.Contains(err.Error(), "junction") {
		t.Fatalf("output through junction: %v", err)
	}

	// Discovered tables and Proto inputs must not follow a junction either.
	tables := copyFixture(t)
	junction(t, filepath.Join(tables, "Tables", "Linked"), outside)
	p, err = Load(tables)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "junction") {
		t.Fatalf("table junction: %v", err)
	}
	protos := copyFixture(t)
	junction(t, filepath.Join(protos, "Protocols", "linked.proto"), outside)
	p, err = Load(protos)
	if err != nil {
		t.Fatal(err)
	}
	p.Config.Tables = nil
	if _, err := p.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "junction") {
		t.Fatalf("Proto junction: %v", err)
	}

	report := t.TempDir()
	junction(t, filepath.Join(report, ReportName), outside)
	if err := WriteReport(report, RunReport{}); err == nil {
		t.Fatal("report followed a junction")
	}
	if !reflect.DeepEqual(before, snapshot(t, outside)) {
		t.Fatal("wrote through a junction")
	}
}
