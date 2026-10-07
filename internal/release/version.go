// Package release shares version and provenance rules between local packagers.
package release

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

func Version(root string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(root, "release.json"))
	if err != nil {
		return "", err
	}
	var config struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return "", err
	}
	if !versionPattern.MatchString(config.Version) {
		return "", fmt.Errorf("release.json: version must be a stable major.minor.patch version")
	}
	return config.Version, nil
}

var jsonManifests = []string{
	"sdk/typescript/package.json", "sdk/typescript/package-lock.json",
	"editors/vscode/package.json", "editors/unity/package.json", "editors/cocos/package.json",
}

// Versions checks package identities without modifying source files. Sync only
// updates the package versions, preserving dependency and format versions.
func Versions(root string, sync bool) error {
	version, err := Version(root)
	if err != nil {
		return err
	}
	type change struct {
		path string
		data []byte
	}
	var changes []change
	var mismatches []string
	for _, name := range append(append([]string{}, jsonManifests...), "sdk/python/pyproject.toml", "sdk/java/pom.xml", "editors/godot/plugin.cfg") {
		path := filepath.Join(root, filepath.FromSlash(name))
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var current []string
		var updated []byte
		if strings.HasSuffix(name, ".json") {
			var manifest map[string]json.RawMessage
			if err := json.Unmarshal(raw, &manifest); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			var v string
			if err := json.Unmarshal(manifest["version"], &v); err != nil {
				return fmt.Errorf("%s: missing version", name)
			}
			current = append(current, v)
			if strings.HasSuffix(name, "package-lock.json") {
				var packages map[string]map[string]json.RawMessage
				if err := json.Unmarshal(manifest["packages"], &packages); err != nil {
					return err
				}
				if packages[""] == nil {
					return fmt.Errorf("%s: missing root package", name)
				}
				if err := json.Unmarshal(packages[""]["version"], &v); err != nil {
					return err
				}
				current = append(current, v)
			}
			updated, err = updateJSONVersions(raw, version, strings.HasSuffix(name, "package-lock.json"))
			if err != nil {
				return err
			}
		} else {
			pattern := `(?m)^(version\s*=\s*")[^"]+("\s*)$`
			if strings.HasSuffix(name, ".xml") {
				var pom struct {
					Version string `xml:"version"`
				}
				if err := xml.Unmarshal(raw, &pom); err != nil {
					return err
				}
				current = append(current, pom.Version)
				pattern = `(<artifactId>tabforge-data</artifactId>\s*<version>)[^<]+(</version>)`
			}
			re := regexp.MustCompile(pattern)
			matches := re.FindAllSubmatch(raw, -1)
			if len(matches) != 1 {
				return fmt.Errorf("%s: expected one package version", name)
			}
			if len(current) == 0 {
				full := string(matches[0][0])
				prefix := string(matches[0][1])
				suffix := string(matches[0][2])
				current = append(current, strings.TrimSuffix(strings.TrimPrefix(full, prefix), suffix))
			}
			updated = re.ReplaceAll(raw, []byte("${1}"+version+"${2}"))
		}
		for _, v := range current {
			if v != version {
				mismatches = append(mismatches, name+": "+v+" != "+version)
			}
		}
		if sync && string(raw) != string(updated) {
			// Preserve original formatting for manifests already at the target version.
			needsUpdate := false
			for _, v := range current {
				needsUpdate = needsUpdate || v != version
			}
			if needsUpdate {
				changes = append(changes, change{path, updated})
			}
		}
	}
	if !sync && len(mismatches) != 0 {
		return fmt.Errorf("release versions differ; run go run ./cmd/release -sync:\n%s", strings.Join(mismatches, "\n"))
	}
	// Parse every manifest before changing any file.
	for _, c := range changes {
		if err := os.WriteFile(c.path, c.data, 0644); err != nil {
			return err
		}
	}
	return nil
}

// Decoder offsets identify the exact string values, so syncing a lockfile does
// not reformat it or accidentally change a dependency with the same version.
func updateJSONVersions(raw []byte, version string, lock bool) ([]byte, error) {
	type span struct{ start, end int }
	var spans []span
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var walk func([]string) error
	walk = func(path []string) error {
		before := int(decoder.InputOffset())
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		if delimiter, ok := token.(json.Delim); ok {
			switch delimiter {
			case '{':
				for decoder.More() {
					key, err := decoder.Token()
					if err != nil {
						return err
					}
					if err := walk(append(append([]string{}, path...), key.(string))); err != nil {
						return err
					}
				}
			case '[':
				for decoder.More() {
					if err := walk(append(append([]string{}, path...), "[]")); err != nil {
						return err
					}
				}
			}
			_, err = decoder.Token()
			return err
		}
		match := len(path) == 1 && path[0] == "version"
		match = match || (lock && len(path) == 3 && path[0] == "packages" && path[1] == "" && path[2] == "version")
		if match {
			if _, ok := token.(string); !ok {
				return fmt.Errorf("package version must be a string")
			}
			end := int(decoder.InputOffset())
			start := before + bytes.IndexByte(raw[before:end], '"')
			spans = append(spans, span{start, end})
		}
		return nil
	}
	if err := walk(nil); err != nil {
		return nil, err
	}
	expected := 1
	if lock {
		expected = 2
	}
	if len(spans) != expected {
		return nil, fmt.Errorf("missing package version values")
	}
	encoded, _ := json.Marshal(version)
	updated := append([]byte{}, raw...)
	for i := len(spans) - 1; i >= 0; i-- {
		s := spans[i]
		next := append([]byte{}, updated[:s.start]...)
		next = append(next, encoded...)
		updated = append(next, updated[s.end:]...)
	}
	return updated, nil
}
