// Command package builds self-contained project ZIPs and VS Code VSIX files.
// It is a maintainer command; delivered projects need no development tools.
package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/Coder-is/TabForge/project"
)

type target struct{ os, arch, vscode string }

var targets = []target{{"darwin", "arm64", "darwin-arm64"}, {"darwin", "amd64", "darwin-x64"}, {"windows", "amd64", "win32-x64"}, {"windows", "arm64", "win32-arm64"}}

func main() {
	out := flag.String("out", "outputs/releases", "release directory")
	selected := flag.String("target", "all", "all, darwin-arm64, darwin-x64, win32-x64 or win32-arm64")
	flag.Parse()
	if err := build(*out, *selected); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func build(out, selected string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return fmt.Errorf("run from the repository root: %w", err)
	}
	if err := os.MkdirAll(out, 0755); err != nil {
		return err
	}
	licenses, err := thirdPartyLicenses(root)
	if err != nil {
		return err
	}
	matched := false
	for _, t := range targets {
		if selected != "all" && selected != t.vscode {
			continue
		}
		matched = true
		fmt.Printf("Building %s...\n", t.vscode)
		stage, err := os.MkdirTemp("", "tabforge-release-")
		if err != nil {
			return err
		}
		err = packageTarget(root, out, stage, t, licenses)
		os.RemoveAll(stage)
		if err != nil {
			return err
		}
	}
	if !matched {
		return fmt.Errorf("unknown target %q", selected)
	}
	var sums strings.Builder
	entries, err := os.ReadDir(out)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || (!strings.HasSuffix(entry.Name(), ".zip") && !strings.HasSuffix(entry.Name(), ".vsix") && !strings.HasSuffix(entry.Name(), ".tgz")) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(out, entry.Name()))
		if err != nil {
			return err
		}
		fmt.Fprintf(&sums, "%x  %s\n", sha256.Sum256(data), entry.Name())
	}
	return os.WriteFile(filepath.Join(out, "SHA256SUMS"), []byte(sums.String()), 0644)
}

func copyTree(source, dest string, allow func(string) bool) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dest, 0755)
		}
		if !allow(filepath.ToSlash(rel)) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return os.MkdirAll(filepath.Join(dest, rel), 0755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		mode := os.FileMode(0644)
		if strings.HasSuffix(rel, ".command") {
			mode = 0755
		}
		return os.WriteFile(filepath.Join(dest, rel), data, mode)
	})
}

func packageTarget(root, out, stage string, t target, licenses map[string][]byte) error {
	projectDir := filepath.Join(stage, "TabForgeProject")
	if err := copyTree(filepath.Join(root, "examples", "complete"), projectDir, func(name string) bool {
		return !strings.HasPrefix(name, "Generated") && !strings.HasPrefix(name, ".tabforge-")
	}); err != nil {
		return err
	}
	name := "tabforge"
	if t.os == "windows" {
		name += ".exe"
	}
	binary := filepath.Join(projectDir, "Tools", "TabForge", name)
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", binary, ".")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+t.os, "GOARCH="+t.arch)
	if data, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("build %s: %w\n%s", t.vscode, err, data)
	}
	if err := os.Chmod(binary, 0755); err != nil {
		return err
	}
	p, err := project.Load(projectDir)
	if err != nil {
		return err
	}
	if _, err := p.Export(context.Background()); err != nil {
		return err
	}
	license, err := os.ReadFile(filepath.Join(root, "LICENSE"))
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(projectDir, "LICENSE"), license, 0644); err != nil {
		return err
	}
	for name, data := range licenses {
		path := filepath.Join(projectDir, "THIRD_PARTY_LICENSES", filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(path, data, 0644); err != nil {
			return err
		}
	}
	var contents bytes.Buffer
	writer := zip.NewWriter(&contents)
	if err := addDirectory(writer, stage, "TabForgeProject"); err != nil {
		writer.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "tabforge-project-"+t.vscode+".zip"), contents.Bytes(), 0644); err != nil {
		return err
	}

	var vsix bytes.Buffer
	writer = zip.NewWriter(&vsix)
	for _, file := range []string{"package.json", "extension.js", "runner.js", "diagnostics.js", "tabforge.schema.json", "README.md"} {
		data, err := os.ReadFile(filepath.Join(root, "editors", "vscode", file))
		if err != nil {
			return err
		}
		if err := addFile(writer, "extension/"+file, data, 0644); err != nil {
			return err
		}
	}
	if err := addFile(writer, "extension/LICENSE", license, 0644); err != nil {
		return err
	}
	licenseNames := make([]string, 0, len(licenses))
	for name := range licenses {
		licenseNames = append(licenseNames, name)
	}
	sort.Strings(licenseNames)
	for _, name := range licenseNames {
		if err := addFile(writer, "extension/THIRD_PARTY_LICENSES/"+name, licenses[name], 0644); err != nil {
			return err
		}
	}
	data, err := os.ReadFile(binary)
	if err != nil {
		return err
	}
	arch := t.arch
	if arch == "amd64" {
		arch = "x64"
	}
	platform := t.os
	if platform == "windows" {
		platform = "win32"
	}
	binaryPath := "extension/bin/" + platform + "-" + arch + "/" + name
	if err := addFile(writer, binaryPath, data, 0755); err != nil {
		return err
	}
	packageData, err := os.ReadFile(filepath.Join(root, "editors", "vscode", "package.json"))
	if err != nil {
		return err
	}
	var extension struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(packageData, &extension); err != nil {
		return err
	}
	manifest := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<PackageManifest Version="2.0.0" xmlns="http://schemas.microsoft.com/developer/vsx-schema/2011" xmlns:d="http://schemas.microsoft.com/developer/vsx-schema-design/2011">
  <Metadata><Identity Language="en-US" Id="tabforge" Version="%s" Publisher="tabforge" TargetPlatform="%s"/><DisplayName>TabForge</DisplayName><Description xml:space="preserve">Portable Excel/CSV and Proto exporter.</Description><Tags>proto,excel,csv</Tags><Categories>Other</Categories><GalleryFlags>Public</GalleryFlags><Properties><Property Id="Microsoft.VisualStudio.Code.Engine" Value="^1.85.0"/><Property Id="Microsoft.VisualStudio.Code.ExtensionDependencies" Value=""/><Property Id="Microsoft.VisualStudio.Code.ExtensionPack" Value=""/><Property Id="Microsoft.VisualStudio.Code.ExecutesCode" Value="true"/></Properties><License>extension/LICENSE</License></Metadata>
  <Installation><InstallationTarget Id="Microsoft.VisualStudio.Code"/></Installation>
  <Dependencies/>
  <Assets><Asset Type="Microsoft.VisualStudio.Code.Manifest" Path="extension/package.json" Addressable="true"/></Assets>
</PackageManifest>`, extension.Version, t.vscode)
	if err := addFile(writer, "extension.vsixmanifest", []byte(manifest), 0644); err != nil {
		return err
	}
	contentTypes := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="json" ContentType="application/json"/><Default Extension="js" ContentType="application/javascript"/><Default Extension="md" ContentType="text/markdown"/><Default Extension="txt" ContentType="text/plain"/><Default Extension="vsixmanifest" ContentType="text/xml"/><Override PartName="/%s" ContentType="application/octet-stream"/><Override PartName="/extension/LICENSE" ContentType="text/plain"/></Types>`, binaryPath)
	if err := addFile(writer, "[Content_Types].xml", []byte(contentTypes), 0644); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "tabforge-"+t.vscode+".vsix"), vsix.Bytes(), 0644); err != nil {
		return err
	}
	if err := packageEditors(root, out, stage, binary, t, license, licenses); err != nil {
		return err
	}
	fmt.Printf("Created project ZIP, VSIX and three engine plugins for %s\n", t.vscode)
	return nil
}

func packageEditors(root, out, stage, binary string, t target, license []byte, licenses map[string][]byte) error {
	data, err := os.ReadFile(binary)
	if err != nil {
		return err
	}
	name := "tabforge"
	if t.os == "windows" {
		name += ".exe"
	}
	for _, kind := range []string{"unity", "cocos", "godot"} {
		base := filepath.Join(stage, "editor-"+kind)
		folder := map[string]string{"unity": "com.tabforge.editor", "cocos": "tabforge", "godot": "addons/tabforge"}[kind]
		dest := filepath.Join(base, filepath.FromSlash(folder))
		if err := copyTree(filepath.Join(root, "editors", kind), dest, func(path string) bool { return path != "bin" && !strings.HasSuffix(path, ".test.cjs") }); err != nil {
			return err
		}
		bin := filepath.Join(dest, "bin", t.vscode, name)
		if err := os.MkdirAll(filepath.Dir(bin), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(bin, data, 0755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dest, "LICENSE"), license, 0644); err != nil {
			return err
		}
		for name, data := range licenses {
			file := filepath.Join(dest, "THIRD_PARTY_LICENSES", filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
				return err
			}
			if err := os.WriteFile(file, data, 0644); err != nil {
				return err
			}
		}
		var contents bytes.Buffer
		writer := zip.NewWriter(&contents)
		if err := addDirectory(writer, base, folder); err != nil {
			writer.Close()
			return err
		}
		if err := writer.Close(); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(out, "tabforge-"+kind+"-"+t.vscode+".zip"), contents.Bytes(), 0644); err != nil {
			return err
		}
	}
	return nil
}

func thirdPartyLicenses(root string) (map[string][]byte, error) {
	cmd := exec.Command("go", "list", "-deps", "-json", ".")
	cmd.Dir = root
	data, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	files := map[string][]byte{}
	seen := map[string]bool{}
	for {
		var pkg struct {
			Module *struct {
				Path, Version, Dir string
				Main               bool
			}
		}
		if err := decoder.Decode(&pkg); err == io.EOF {
			break
		} else if err != nil {
			return nil, err
		}
		module := pkg.Module
		if module == nil || module.Main || seen[module.Path] {
			continue
		}
		seen[module.Path] = true
		if module.Dir == "" {
			return nil, fmt.Errorf("module source unavailable for license collection: %s", module.Path)
		}
		for _, name := range []string{"LICENSE", "LICENSE.txt", "LICENSE.md", "COPYING", "NOTICE", "NOTICE.txt"} {
			data, err := os.ReadFile(filepath.Join(module.Dir, name))
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			files[module.Path+"@"+module.Version+"/"+name+".txt"] = data
		}
	}
	license, err := os.ReadFile(filepath.Join(runtime.GOROOT(), "LICENSE"))
	if err != nil {
		return nil, err
	}
	files["go/"+runtime.Version()+"/LICENSE.txt"] = license
	return files, nil
}

func addDirectory(writer *zip.Writer, root, name string) error {
	return filepath.WalkDir(filepath.Join(root, name), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return addFile(writer, filepath.ToSlash(rel), data, info.Mode())
	})
}

func addFile(writer *zip.Writer, name string, data []byte, mode os.FileMode) error {
	header := &zip.FileHeader{Name: name, Method: zip.Deflate}
	header.SetMode(mode)
	header.SetModTime(time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC))
	f, err := writer.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, bytes.NewReader(data))
	return err
}
