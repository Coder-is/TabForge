// Package project is the reusable project exporter used by the CLI and editors.
// All paths resolve from tabforge.json, never from the process working directory.
package project

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Coder-is/TabForge/protocol"
)

type Config struct {
	Version  int           `json:"version"`
	Output   string        `json:"output,omitempty"`
	Schema   *SchemaConfig `json:"schema,omitempty"`
	Tables   []TableConfig `json:"tables,omitempty"`
	Protocol string        `json:"protocol,omitempty"`
}

type SchemaConfig struct {
	Dir     string   `json:"dir,omitempty"`
	Files   []string `json:"files,omitempty"`
	Imports []string `json:"imports,omitempty"`
	Go      bool     `json:"go,omitempty"`
}

type TableConfig struct {
	Index    string            `json:"index,omitempty"`
	Discover *DiscoverConfig   `json:"discover,omitempty"`
	Mapping  string            `json:"mapping,omitempty"`
	Package  string            `json:"package,omitempty"`
	Root     string            `json:"root,omitempty"`
	Tags     string            `json:"tags,omitempty"`
	Outputs  map[string]string `json:"outputs"`
}

type Project struct {
	Root       string
	ConfigPath string
	Config     Config
}

// Find walks upward from a file or directory to find the project convention.
func Find(start string) (string, error) {
	path, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	if stat, err := os.Stat(path); err == nil && !stat.IsDir() {
		path = filepath.Dir(path)
	}
	for {
		candidate := filepath.Join(path, "tabforge.json")
		if stat, err := os.Stat(candidate); err == nil && !stat.IsDir() {
			return candidate, nil
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", fmt.Errorf("没有找到 tabforge.json：请打开项目，或使用 -init=目录 创建示例项目")
		}
		path = parent
	}
}

func Load(path string) (*Project, error) {
	if stat, err := os.Stat(path); err == nil && stat.IsDir() {
		path = filepath.Join(path, "tabforge.json")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := protocol.ValidateJSON(data); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var config Config
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&config); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("%s: expected one JSON object", path)
	}
	if config.Version != 1 {
		return nil, fmt.Errorf("%s: version must be 1", path)
	}
	if config.Output == "" {
		config.Output = "Generated"
	}
	if config.Schema == nil && len(config.Tables) == 0 {
		return nil, fmt.Errorf("%s: specify schema or tables", path)
	}
	if config.Protocol != "" && config.Schema == nil {
		return nil, fmt.Errorf("protocol requires schema")
	}
	if config.Schema != nil && config.Schema.Dir == "" {
		config.Schema.Dir = "Protocols"
	}
	p := &Project{Root: filepath.Dir(path), ConfigPath: path, Config: config}
	if err := p.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return p, nil
}

// relativePath deliberately requires portable project paths and rejects any
// symlink components: outputs must never replace a source or an external tree.
func relativePath(root, name string) (string, error) {
	if name == "" || strings.Contains(name, "\\") || strings.Contains(name, ":") || filepath.IsAbs(name) {
		return "", fmt.Errorf("use a relative slash-separated path: %q", name)
	}
	clean := filepath.Clean(filepath.FromSlash(name))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path must stay inside the project: %q", name)
	}
	path := filepath.Join(root, clean)
	current := root
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("symlink path is not supported: %s", current)
		}
	}
	return path, nil
}

func overlaps(a, b string) bool {
	a, b = strings.ToLower(filepath.Clean(a)), strings.ToLower(filepath.Clean(b))
	return a == b || strings.HasPrefix(a, b+string(filepath.Separator)) || strings.HasPrefix(b, a+string(filepath.Separator))
}

func (p *Project) validate() error {
	out, err := relativePath(p.Root, p.Config.Output)
	if err != nil {
		return err
	}
	inputs := []string{p.ConfigPath, filepath.Join(p.Root, "Tools"), filepath.Join(p.Root, "Clients"), filepath.Join(p.Root, ".git")}
	if p.Config.Schema != nil {
		s := p.Config.Schema
		for _, name := range append([]string{s.Dir}, s.Imports...) {
			path, err := relativePath(p.Root, name)
			if err != nil {
				return err
			}
			inputs = append(inputs, path)
		}
		for _, name := range s.Files {
			if _, err := relativePath(filepath.Join(p.Root, s.Dir), name); err != nil {
				return err
			}
		}
	}
	if p.Config.Protocol != "" {
		path, err := relativePath(p.Root, p.Config.Protocol)
		if err != nil {
			return err
		}
		inputs = append(inputs, path)
	}
	paths := []string{"export.json"}
	if p.Config.Schema != nil {
		paths = append(paths, "schema", "data_manifest.json")
	}
	if p.Config.Protocol != "" {
		paths = append(paths, "protocol")
	}
	for _, tab := range p.Config.Tables {
		if (tab.Index == "") == (tab.Discover == nil) {
			return fmt.Errorf("table job requires exactly one of index or discover")
		}
		var source string
		if tab.Discover != nil {
			source = tab.Discover.Dir
		} else {
			source = tab.Index
		}
		index, err := relativePath(p.Root, source)
		if err != nil {
			return err
		}
		// Table references resolve from the index directory; protect that whole tree.
		if tab.Discover != nil {
			if err := tab.Discover.validate(p.Root); err != nil {
				return err
			}
			inputs = append(inputs, index)
		} else {
			inputs = append(inputs, filepath.Dir(index))
		}
		if tab.Mapping != "" {
			if p.Config.Schema == nil {
				return fmt.Errorf("%s: mapping requires schema", tab.Index)
			}
			path, err := relativePath(p.Root, tab.Mapping)
			if err != nil {
				return err
			}
			inputs = append(inputs, path)
		}
		if tab.Root == "" {
			tab.Root = "Table"
		}
		if len(tab.Outputs) == 0 {
			return fmt.Errorf("%s: no outputs", tab.Index)
		}
		for kind, name := range tab.Outputs {
			if !knownOutput(kind) {
				return fmt.Errorf("%s: unknown output %q", tab.Index, kind)
			}
			if kind == "protojson" && tab.Mapping == "" {
				return fmt.Errorf("%s: protojson requires mapping", tab.Index)
			}
			if kind == "proto" && tab.Mapping != "" {
				return fmt.Errorf("%s: proto output conflicts with existing Proto mapping", tab.Index)
			}
			if _, err := relativePath(out, name); err != nil {
				return err
			}
			for _, previous := range paths {
				if overlaps(name, previous) {
					return fmt.Errorf("overlapping outputs: %q and %q", name, previous)
				}
			}
			paths = append(paths, name)
		}
	}
	for _, input := range inputs {
		if overlaps(out, input) {
			return fmt.Errorf("output %q overlaps source/tool directory %s", p.Config.Output, input)
		}
	}
	return nil
}
