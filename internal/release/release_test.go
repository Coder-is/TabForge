package release

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(name, data string) {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("release.json", `{"version":"0.4.0"}`)
	for _, name := range jsonManifests {
		write(name, "{\n  \"name\":\"sample\", \"version\": \"0.3.0\", \"dependencies\":{\"thing\":\"0.3.0\"}\n}\n")
	}
	write("sdk/typescript/package-lock.json", `{"packages":{"node_modules/thing":{"version":"0.3.0"},"":{"dependencies":{"thing":"0.3.0"},"version":"0.3.0"}},"version":"0.3.0"}`)
	write("sdk/python/pyproject.toml", "[project]\nversion = \"0.3.0\"\n")
	write("sdk/java/pom.xml", `<project><artifactId>tabforge-data</artifactId><version>0.3.0</version><dependencies><dependency><version>0.3.0</version></dependency></dependencies></project>`)
	write("editors/godot/plugin.cfg", "[plugin]\nversion=\"0.3.0\"\n")
	return root
}

func TestSyncPreservesFormattingAndDependencies(t *testing.T) {
	root := fixture(t)
	if err := Versions(root, false); err == nil {
		t.Fatal("accepted inconsistent versions")
	}
	if err := Versions(root, true); err != nil {
		t.Fatal(err)
	}
	if err := Versions(root, false); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(root, "sdk/typescript/package-lock.json"))
	want := `{"packages":{"node_modules/thing":{"version":"0.3.0"},"":{"dependencies":{"thing":"0.3.0"},"version":"0.4.0"}},"version":"0.4.0"}`
	if string(raw) != want {
		t.Fatalf("lockfile changed beyond root version: %s", raw)
	}
	packageRaw, _ := os.ReadFile(filepath.Join(root, "sdk/typescript/package.json"))
	if string(packageRaw) != "{\n  \"name\":\"sample\", \"version\": \"0.4.0\", \"dependencies\":{\"thing\":\"0.3.0\"}\n}\n" {
		t.Fatalf("package formatting changed: %s", packageRaw)
	}
	if err := Versions(root, true); err != nil {
		t.Fatal(err)
	}
	again, _ := os.ReadFile(filepath.Join(root, "sdk/typescript/package-lock.json"))
	if string(again) != want {
		t.Fatal("sync not idempotent")
	}
}

func TestInvalidManifestDoesNotPartiallySync(t *testing.T) {
	root := fixture(t)
	path := filepath.Join(root, "sdk/typescript/package.json")
	before, _ := os.ReadFile(path)
	if err := os.WriteFile(filepath.Join(root, "editors/godot/plugin.cfg"), []byte("[plugin]\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := Versions(root, true); err == nil {
		t.Fatal("accepted missing version")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("parse failure partially modified versions")
	}
}

func TestBuildInfoRejectsInvalidValues(t *testing.T) {
	root := fixture(t)
	path := filepath.Join(root, "build-info.json")
	info := Info{"0.4.0", strings.Repeat("a", 40), true, "2026-10-07T01:02:03Z"}
	if err := WriteJSON(path, info); err != nil {
		t.Fatal(err)
	}
	loaded, err := BuildInfo(root, path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(loaded.LDFlags(), strings.Repeat("a", 40)+"-dirty") {
		t.Fatal("dirty provenance missing")
	}
	for _, invalid := range []Info{
		{"0.3.0", info.GitCommit, false, info.BuildTime},
		{"0.4.0", "commit -X injected=value", false, info.BuildTime},
		{"0.4.0", info.GitCommit, false, "not-a-time"},
	} {
		if err := WriteJSON(path, invalid); err != nil {
			t.Fatal(err)
		}
		if _, err := BuildInfo(root, path); err == nil {
			t.Fatalf("accepted invalid info: %+v", invalid)
		}
	}
}

func TestStableVersionOnly(t *testing.T) {
	root := fixture(t)
	for _, version := range []string{"0.4.0-beta.1", "04.0.0", "v0.4.0", "0.4.0 -X anything"} {
		raw, _ := json.Marshal(map[string]string{"version": version})
		if err := os.WriteFile(filepath.Join(root, "release.json"), raw, 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := Version(root); err == nil {
			t.Fatalf("accepted %s", version)
		}
	}
}

func TestChecksumsIncludeReportsAndRejectDirectories(t *testing.T) {
	out := t.TempDir()
	if err := os.WriteFile(filepath.Join(out, "validation.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := WriteSums(out); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(out, "SHA256SUMS"))
	if string(raw) != "44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a  validation.json\n" {
		t.Fatalf("wrong checksum: %s", raw)
	}
	if err := os.Mkdir(filepath.Join(out, "unexpected"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := WriteSums(out); err == nil {
		t.Fatal("accepted nested release directory")
	}
}

func TestStandaloneChecksumsAllowExistingVersionDirectories(t *testing.T) {
	out := t.TempDir()
	if err := os.Mkdir(filepath.Join(out, "0.3.0"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "package.zip"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := WritePackageSums(out); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(out, "SHA256SUMS"))
	if !strings.HasSuffix(string(raw), "  package.zip\n") {
		t.Fatalf("missing package checksum: %s", raw)
	}
}
