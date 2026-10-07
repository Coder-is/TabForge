package project

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Coder-is/TabForge/v3/model"
)

// Discovery rules are configured once by programmers. Planners can add and
// remove files matching those rules without maintaining an index.
type DiscoverConfig struct {
	Dir     string         `json:"dir"`
	Types   []string       `json:"types"`
	Rules   []DiscoverRule `json:"rules"`
	Exclude []string       `json:"exclude,omitempty"`
}

type DiscoverRule struct {
	Pattern string `json:"pattern"`
	Type    string `json:"type"`
	Mode    string `json:"mode,omitempty"` // data (default) or kv
}

type DiscoveryError struct{ Path, Reason string }

func (e *DiscoveryError) Error() string { return e.Path + ": " + e.Reason }

func validatePattern(pattern string) error {
	if pattern == "" || strings.ContainsAny(pattern, "\\:") || strings.HasPrefix(pattern, "/") {
		return fmt.Errorf("use relative slash-separated discovery pattern: %q", pattern)
	}
	for _, part := range strings.Split(pattern, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("unsafe discovery pattern: %q", pattern)
		}
		if strings.Contains(part, "**") && part != "**" {
			return fmt.Errorf("** must be a complete path segment: %q", pattern)
		}
		if _, err := path.Match(part, "test"); err != nil {
			return fmt.Errorf("invalid discovery pattern %q: %w", pattern, err)
		}
	}
	return nil
}

// ** matches zero or more complete directory segments; other segments use Go
// path.Match syntax. Paths and patterns have the same meaning on Win and Mac.
func matchPattern(pattern, name string) bool {
	p, n := strings.Split(pattern, "/"), strings.Split(name, "/")
	var match func(int, int) bool
	memo := map[[2]int]bool{}
	seen := map[[2]int]bool{}
	match = func(i, j int) bool {
		key := [2]int{i, j}
		if seen[key] {
			return memo[key]
		}
		seen[key] = true
		var ok bool
		if i == len(p) {
			ok = j == len(n)
		} else if p[i] == "**" {
			ok = match(i+1, j) || (j < len(n) && match(i, j+1))
		} else if j < len(n) {
			part, _ := path.Match(p[i], n[j])
			ok = part && match(i+1, j+1)
		}
		memo[key] = ok
		return ok
	}
	return match(0, 0)
}

func (d *DiscoverConfig) validate(root string) error {
	dir, err := relativePath(root, d.Dir)
	if err != nil {
		return err
	}
	if len(d.Types) == 0 || len(d.Rules) == 0 {
		return fmt.Errorf("discover requires types and rules")
	}
	seen := map[string]bool{}
	for _, name := range d.Types {
		if _, err := relativePath(dir, name); err != nil {
			return err
		}
		if !tableExtension(name) {
			return fmt.Errorf("unsupported type file %s", name)
		}
		key := strings.ToLower(name)
		if seen[key] {
			return fmt.Errorf("duplicate discovered type file %s", name)
		}
		seen[key] = true
	}
	for _, rule := range d.Rules {
		if err := validatePattern(rule.Pattern); err != nil {
			return err
		}
		if rule.Type == "" || (rule.Mode != "" && rule.Mode != "data" && rule.Mode != "kv") {
			return fmt.Errorf("discovery rule needs type and mode data/kv")
		}
	}
	for _, pattern := range d.Exclude {
		if err := validatePattern(pattern); err != nil {
			return err
		}
	}
	return nil
}

func tableExtension(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".csv", ".xlsx", ".xlsm":
		return true
	}
	return false
}

func (p *Project) discover(d *DiscoverConfig) ([]*model.IndexDefine, error) {
	dir, err := relativePath(p.Root, d.Dir)
	if err != nil {
		return nil, err
	}
	var result []*model.IndexDefine
	types := map[string]bool{}
	for _, name := range d.Types {
		file, err := relativePath(dir, name)
		if err != nil {
			return nil, err
		}
		stat, err := os.Stat(file)
		if err != nil {
			return nil, err
		}
		if !stat.Mode().IsRegular() {
			return nil, fmt.Errorf("type input is not a regular file: %s", file)
		}
		types[name] = true
		result = append(result, &model.IndexDefine{Kind: model.TableKind_Type, TableType: "TypeDefine", TableFileName: name})
	}
	seen := map[string]string{}
	for name := range types {
		seen[strings.ToLower(name)] = name
	}
	matched := make([]int, len(d.Rules))
	err = filepath.WalkDir(dir, func(file string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink table input: %s", file)
		}
		if file == dir {
			return nil
		}
		name, err := filepath.Rel(dir, file)
		if err != nil {
			return err
		}
		name = filepath.ToSlash(name)
		base := entry.Name()
		if strings.HasPrefix(base, ".") || strings.HasPrefix(base, "~$") || strings.HasSuffix(base, "~") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		for _, pattern := range d.Exclude {
			if matchPattern(pattern, name) {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("table input is not a regular file: %s", file)
		}
		if !tableExtension(name) {
			return nil
		}
		if types[name] {
			return nil
		}
		// Historical indexes may remain as documentation; they are never loaded.
		if strings.EqualFold(strings.TrimSuffix(base, filepath.Ext(base)), "Index") {
			return nil
		}
		key := strings.ToLower(name)
		if old, ok := seen[key]; ok {
			return fmt.Errorf("case-colliding inputs %s and %s", old, name)
		}
		seen[key] = name
		found := -1
		for i, rule := range d.Rules {
			if matchPattern(rule.Pattern, name) {
				if found >= 0 {
					return &DiscoveryError{Path: file, Reason: "matches multiple discovery rules"}
				}
				found = i
			}
		}
		if found < 0 {
			return &DiscoveryError{Path: file, Reason: "no discovery rule; put the table in its configured directory or ask a programmer to add its type rule"}
		}
		rule := d.Rules[found]
		kind := model.TableKind_Data
		if rule.Mode == "kv" {
			kind = model.TableKind_KeyValue
		}
		result = append(result, &model.IndexDefine{Kind: kind, TableType: rule.Type, TableFileName: name})
		matched[found]++
		return nil
	})
	if err != nil {
		return nil, err
	}
	for i, count := range matched {
		if count == 0 {
			return nil, fmt.Errorf("discovery rule %q matched no tables", d.Rules[i].Pattern)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		a, b := result[i], result[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.TableType != b.TableType {
			return a.TableType < b.TableType
		}
		return a.TableFileName < b.TableFileName
	})
	return result, nil
}
