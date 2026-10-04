// Command package-backend builds local Go, npm, Python and Maven distributions.
// Registry publication and Git tags are intentionally separate release actions.
package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

func main() {
	out := flag.String("out", "outputs/releases/v3", "local release directory")
	maven := flag.String("maven", "mvn", "Maven executable")
	python := flag.String("python", "python3", "Python executable")
	npm := flag.String("npm", "npm", "npm executable")
	repo := flag.String("maven-repo", "", "optional Maven dependency cache directory")
	flag.Parse()
	if err := build(*out, *maven, *python, *npm, *repo); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(root, executable string, args ...string) error {
	cmd, err := command(root, executable, args)
	if err != nil {
		return err
	}
	cmd.Dir = root
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", executable, err)
	}
	return nil
}

// Windows npm/Maven entry points are batch files. Launch their real Node/Java
// programs so spaces, Unicode and shell characters remain literal arguments.
func command(root, executable string, args []string) (*exec.Cmd, error) {
	resolved, err := exec.LookPath(executable)
	if err != nil {
		return nil, err
	}
	ext := strings.ToLower(filepath.Ext(resolved))
	if runtime.GOOS != "windows" || (ext != ".cmd" && ext != ".bat") {
		return exec.Command(resolved, args...), nil
	}
	name := strings.ToLower(strings.TrimSuffix(filepath.Base(resolved), filepath.Ext(resolved)))
	switch name {
	case "npm":
		cli := filepath.Join(filepath.Dir(resolved), "node_modules", "npm", "bin", "npm-cli.js")
		if _, err := os.Stat(cli); err != nil {
			return nil, fmt.Errorf("cannot find npm CLI beside %s: %w", resolved, err)
		}
		return exec.Command("node", append([]string{cli}, args...)...), nil
	case "mvn":
		home := filepath.Dir(filepath.Dir(resolved))
		jars, err := filepath.Glob(filepath.Join(home, "boot", "plexus-classworlds-*.jar"))
		if err != nil || len(jars) != 1 {
			return nil, fmt.Errorf("cannot find Maven launcher under %s", home)
		}
		javaArgs := []string{"-Dmaven.home=" + home, "-Dclassworlds.conf=" + filepath.Join(home, "bin", "m2.conf"), "-Dmaven.multiModuleProjectDirectory=" + root, "-classpath", jars[0], "org.codehaus.plexus.classworlds.launcher.Launcher"}
		return exec.Command("java", append(javaArgs, args...)...), nil
	default:
		return nil, fmt.Errorf("unsupported batch tool %s; use a native executable", resolved)
	}
}

func build(out, maven, python, npm, mavenRepo string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	out, err = filepath.Abs(out)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(out, 0755); err != nil {
		return err
	}
	var version struct {
		Version string `json:"version"`
	}
	raw, err := os.ReadFile(filepath.Join(root, "sdk/typescript/package.json"))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &version); err != nil {
		return err
	}
	if err := run(filepath.Join(root, "sdk", "typescript"), npm, "pack", "--pack-destination", out); err != nil {
		return err
	}
	if err := run(root, python, "-m", "pip", "wheel", "--no-deps", "--no-build-isolation", "--wheel-dir", out, "sdk/python"); err != nil {
		return err
	}
	args := []string{"-B", "-q", "-f", "sdk/java/pom.xml", "package", "org.apache.maven.plugins:maven-dependency-plugin:3.8.1:copy-dependencies", "-DincludeScope=runtime"}
	if mavenRepo != "" {
		args = append(args, "-Dmaven.repo.local="+mavenRepo)
	}
	if err := run(root, maven, args...); err != nil {
		return err
	}
	name := "tabforge-data-" + version.Version
	for _, pair := range [][2]string{{"sdk/java/target/" + name + ".jar", name + ".jar"}, {"sdk/java/pom.xml", name + ".pom"}} {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(pair[0])))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(out, pair[1]), data, 0644); err != nil {
			return err
		}
	}
	if err := goArchive(root, filepath.Join(out, "tabforge-go-"+version.Version+".zip")); err != nil {
		return err
	}
	var sums strings.Builder
	files, err := os.ReadDir(out)
	if err != nil {
		return err
	}
	for _, file := range files {
		if file.IsDir() || file.Name() == "SHA256SUMS" || (!strings.HasSuffix(file.Name(), ".jar") && !strings.HasSuffix(file.Name(), ".pom") && !strings.HasSuffix(file.Name(), ".tgz") && !strings.HasSuffix(file.Name(), ".whl") && !strings.HasSuffix(file.Name(), ".zip") && !strings.HasSuffix(file.Name(), ".vsix")) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(out, file.Name()))
		if err != nil {
			return err
		}
		fmt.Fprintf(&sums, "%x  %s\n", sha256.Sum256(data), file.Name())
	}
	return os.WriteFile(filepath.Join(out, "SHA256SUMS"), []byte(sums.String()), 0644)
}

// This source archive is a Go module usable via replace, containing the runtime
// packages only. The full repository remains the build-time project library.
func goArchive(root, out string) (err error) {
	var names []string
	for _, dir := range []string{"databundle", "protocol", "sdk/typescript"} {
		err := filepath.WalkDir(filepath.Join(root, filepath.FromSlash(dir)), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.Type()&fs.ModeSymlink != 0 {
				return fmt.Errorf("symlink runtime source: %s", path)
			}
			if entry.IsDir() {
				if entry.Name() == "node_modules" || entry.Name() == "dist" {
					return filepath.SkipDir
				}
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if strings.HasSuffix(rel, "_test.go") {
				return nil
			}
			if filepath.Ext(rel) == ".go" || rel == filepath.Join("sdk", "typescript", "data.ts") || rel == filepath.Join("sdk", "typescript", "schema.ts") || rel == filepath.Join("sdk", "typescript", "json.ts") {
				names = append(names, filepath.ToSlash(rel))
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	names = append(names, "go.mod", "go.sum", "LICENSE", "doc/backend-workflow.md")
	sort.Strings(names)
	file, err := os.Create(out)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
	}()
	w := zip.NewWriter(file)
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			w.Close()
			return err
		}
		h := &zip.FileHeader{Name: "tabforge-go/" + name, Method: zip.Deflate}
		h.SetMode(0644)
		entry, err := w.CreateHeader(h)
		if err != nil {
			w.Close()
			return err
		}
		if _, err := entry.Write(data); err != nil {
			w.Close()
			return err
		}
	}
	return w.Close()
}
