// Command release builds and validates a complete local delivery directory.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/Coder-is/TabForge/internal/filelock"
	"github.com/Coder-is/TabForge/internal/release"
)

type options struct {
	out, target, python, maven, npm, mavenRepo string
	clean                                      bool
}

func main() {
	var o options
	flag.StringVar(&o.out, "out", "", "new delivery directory; default outputs/releases/<version>")
	flag.StringVar(&o.target, "target", "all", "all, darwin-arm64, darwin-x64, win32-x64 or win32-arm64")
	flag.StringVar(&o.python, "python", "python3", "Python executable for packaging and verification")
	flag.StringVar(&o.maven, "maven", "mvn", "Maven executable")
	flag.StringVar(&o.npm, "npm", "npm", "npm executable")
	flag.StringVar(&o.mavenRepo, "maven-repo", "", "optional Maven dependency cache directory")
	flag.BoolVar(&o.clean, "require-clean", false, "refuse uncommitted source when building a public delivery")
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
			// Ctrl+C or termination stops the steps and still removes the
			// stage directory and lock instead of killing the process outright.
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			err = build(ctx, root, o)
			stop()
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

func run(ctx context.Context, root, executable string, args ...string) error {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = root
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", executable, err)
	}
	return nil
}

func build(ctx context.Context, root string, o options) error {
	return buildUsing(ctx, root, o, func(dir, executable string, args ...string) error {
		return run(ctx, dir, executable, args...)
	})
}

// stagePrefix names build directories after their destination, so a run that
// holds the destination's lock can recognize stages left by killed runs.
func stagePrefix(out string) string {
	return "." + filepath.Base(out) + ".tabforge-release-"
}

// removeStaleStages deletes stage directories of earlier runs for out. The
// caller holds out's lock, so no live run can still be using them.
func removeStaleStages(out string) error {
	parent, prefix := filepath.Dir(out), stagePrefix(out)
	entries, err := os.ReadDir(parent)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		suffix, ok := strings.CutPrefix(entry.Name(), prefix)
		// os.MkdirTemp appends only decimal digits.
		if !ok || suffix == "" || strings.Trim(suffix, "0123456789") != "" || !entry.IsDir() {
			continue
		}
		if err := os.RemoveAll(filepath.Join(parent, entry.Name())); err != nil {
			return fmt.Errorf("remove stale release stage: %w", err)
		}
	}
	return nil
}

func buildUsing(ctx context.Context, root string, o options, execute func(string, string, ...string) error) error {
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
	if o.clean && info.Dirty {
		return fmt.Errorf("public delivery requires a clean Git working tree")
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
	// It records its owner, so one left by a killed run is reclaimed.
	lockPath := out + ".lock"
	lock, err := filelock.Acquire(lockPath)
	if err != nil {
		if info, statErr := os.Lstat(lockPath); statErr == nil && info.IsDir() {
			return fmt.Errorf("acquire delivery lock: %s is a directory lock from an older release tool; remove it if no release is running", lockPath)
		}
		return fmt.Errorf("acquire delivery lock: %w", err)
	}
	defer lock.Release()
	if err := removeStaleStages(out); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(filepath.Dir(out), stagePrefix(out))
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	// Stop between steps once interrupted; the deferred cleanup still runs.
	inner := execute
	execute = func(dir, executable string, args ...string) error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("release interrupted: %w", err)
		}
		return inner(dir, executable, args...)
	}
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
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("release interrupted: %w", err)
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
