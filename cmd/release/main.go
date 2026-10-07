// Command release builds and validates a complete local delivery directory.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/Coder-is/TabForge/internal/release"
)

type options struct{ out, target, python, maven, npm, mavenRepo string }

func main() {
	var o options
	flag.StringVar(&o.out, "out", "", "new delivery directory; default outputs/releases/<version>")
	flag.StringVar(&o.target, "target", "all", "all, darwin-arm64, darwin-x64, win32-x64 or win32-arm64")
	flag.StringVar(&o.python, "python", "python3", "Python executable for packaging and verification")
	flag.StringVar(&o.maven, "maven", "mvn", "Maven executable")
	flag.StringVar(&o.npm, "npm", "npm", "npm executable")
	flag.StringVar(&o.mavenRepo, "maven-repo", "", "optional Maven dependency cache directory")
	check := flag.Bool("check", false, "check version consistency without building")
	sync := flag.Bool("sync", false, "sync package versions from release.json without building")
	flag.Parse()
	root, err := os.Getwd()
	if err == nil {
		if *check && *sync {
			err = fmt.Errorf("-check and -sync cannot be combined")
		} else if *check || *sync {
			err = release.Versions(root, *sync)
		} else {
			err = build(root, o)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *check || *sync {
		version, _ := release.Version(root)
		fmt.Println("Release versions consistent:", version)
	}
}

func run(root, executable string, args ...string) error {
	cmd := exec.Command(executable, args...)
	cmd.Dir = root
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", executable, err)
	}
	return nil
}

func build(root string, o options) error {
	return buildUsing(root, o, run)
}

func buildUsing(root string, o options, execute func(string, string, ...string) error) error {
	if err := release.Versions(root, false); err != nil {
		return err
	}
	valid := map[string]bool{"all": true, "darwin-arm64": true, "darwin-x64": true, "win32-x64": true, "win32-arm64": true}
	if !valid[o.target] {
		return fmt.Errorf("unknown target %q", o.target)
	}
	info, err := release.BuildInfo(root, "")
	if err != nil {
		return err
	}
	if o.out == "" {
		o.out = filepath.Join(root, "outputs", "releases", info.Version)
	}
	out, err := filepath.Abs(o.out)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(out); !os.IsNotExist(err) {
		return fmt.Errorf("delivery directory must not exist: %s", out)
	}
	if err := os.MkdirAll(filepath.Dir(out), 0755); err != nil {
		return err
	}
	// Cooperative lock covers building and publication to the same destination.
	lock := out + ".lock"
	if err := os.Mkdir(lock, 0755); err != nil {
		return fmt.Errorf("acquire delivery lock %s: %w", lock, err)
	}
	defer os.Remove(lock)
	stage, err := os.MkdirTemp(filepath.Dir(out), ".tabforge-release-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	infoPath := filepath.Join(stage, "build-info.json")
	if err := release.WriteJSON(infoPath, info); err != nil {
		return err
	}
	if err := execute(root, "go", "run", "./cmd/package", "-out="+stage, "-target="+o.target, "-build-info="+infoPath); err != nil {
		return err
	}
	if err := execute(root, "go", "run", "./cmd/package-backend", "-out="+stage, "-python="+o.python, "-maven="+o.maven, "-npm="+o.npm, "-maven-repo="+o.mavenRepo); err != nil {
		return err
	}
	if err := execute(root, o.python, "scripts/verify_backend.py", "--out", stage, "--npm", o.npm); err != nil {
		return err
	}
	if err := release.WriteSums(stage); err != nil {
		return err
	}
	if err := execute(root, o.python, "scripts/verify_release.py", "--out", stage, "--target", o.target); err != nil {
		return err
	}
	install, err := os.ReadFile(filepath.Join(root, "doc", "release-workflow.md"))
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stage, "INSTALL.md"), install, 0644); err != nil {
		return err
	}
	artifacts, err := release.Artifacts(stage)
	if err != nil {
		return err
	}
	manifest := struct {
		Format string `json:"format"`
		release.Info
		Target              string             `json:"target"`
		BackendValidation   string             `json:"backendValidation"`
		ClientValidation    string             `json:"clientValidation"`
		RegistriesPublished bool               `json:"registriesPublished"`
		Artifacts           []release.Artifact `json:"artifacts"`
	}{"tabforge.release.v1", info, o.target, "validation.json", "client-validation.json", false, artifacts}
	if err := release.WriteJSON(filepath.Join(stage, "release.json"), manifest); err != nil {
		return err
	}
	if err := release.WriteSums(stage); err != nil {
		return err
	}
	if _, err := os.Lstat(out); !os.IsNotExist(err) {
		return fmt.Errorf("delivery destination appeared during build: %s", out)
	}
	if err := os.Rename(stage, out); err != nil {
		return fmt.Errorf("publish validated delivery: %w", err)
	}
	fmt.Println("Validated delivery:", out)
	return nil
}
