package release

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Info struct {
	Version   string `json:"version"`
	GitCommit string `json:"gitCommit"`
	Dirty     bool   `json:"dirty"`
	BuildTime string `json:"buildTime"`
}

// BuildInfo captures one provenance record for all binaries in a release.
// SOURCE_DATE_EPOCH allows callers to choose a reproducible timestamp.
func BuildInfo(root, from string) (Info, error) {
	version, err := Version(root)
	if err != nil {
		return Info{}, err
	}
	var info Info
	if from != "" {
		raw, err := os.ReadFile(from)
		if err != nil {
			return info, err
		}
		if err := json.Unmarshal(raw, &info); err != nil {
			return info, err
		}
	} else {
		git := func(args ...string) (string, error) {
			cmd := exec.Command("git", args...)
			cmd.Dir = root
			raw, err := cmd.Output()
			return strings.TrimSpace(string(raw)), err
		}
		info.Version = version
		info.GitCommit, err = git("rev-parse", "HEAD")
		if err != nil {
			return info, fmt.Errorf("read release commit: %w", err)
		}
		status, err := git("status", "--porcelain")
		if err != nil {
			return info, err
		}
		info.Dirty = status != ""
		now := time.Now().UTC()
		if epoch := os.Getenv("SOURCE_DATE_EPOCH"); epoch != "" {
			seconds, err := strconv.ParseInt(epoch, 10, 64)
			if err != nil || seconds < 0 {
				return info, fmt.Errorf("invalid SOURCE_DATE_EPOCH %q", epoch)
			}
			now = time.Unix(seconds, 0).UTC()
		}
		info.BuildTime = now.Format(time.RFC3339)
	}
	if info.Version != version || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(info.GitCommit) {
		return info, fmt.Errorf("invalid build-info version or commit")
	}
	if _, err := time.Parse(time.RFC3339, info.BuildTime); err != nil {
		return info, err
	}
	return info, nil
}

func (info Info) LDFlags() string {
	commit := info.GitCommit
	if info.Dirty {
		commit += "-dirty"
	}
	const pkg = "github.com/Coder-is/TabForge/build."
	return "-s -w -X " + pkg + "Version=" + info.Version + " -X " + pkg + "GitCommit=" + commit + " -X " + pkg + "BuildTime=" + info.BuildTime
}

func WriteJSON(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0644)
}

type Artifact struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Artifacts includes only immediate regular files; nested directories and
// symlinks cannot silently become part of a release's integrity record.
func Artifacts(out string) ([]Artifact, error) {
	entries, err := os.ReadDir(out)
	if err != nil {
		return nil, err
	}
	artifacts := make([]Artifact, 0, len(entries))
	for _, entry := range entries {
		if entry.Name() == "SHA256SUMS" {
			continue
		}
		if strings.ContainsAny(entry.Name(), "\r\n") {
			return nil, fmt.Errorf("invalid release file name")
		}
		if !entry.Type().IsRegular() {
			return nil, fmt.Errorf("unexpected release entry %s", entry.Name())
		}
		file, err := os.Open(filepath.Join(out, entry.Name()))
		if err != nil {
			return nil, err
		}
		hash := sha256.New()
		size, err := io.Copy(hash, file)
		closeErr := file.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		artifacts = append(artifacts, Artifact{entry.Name(), size, fmt.Sprintf("%x", hash.Sum(nil))})
	}
	return artifacts, nil
}

func WriteSums(out string) error {
	artifacts, err := Artifacts(out)
	if err != nil {
		return err
	}
	var sums strings.Builder
	for _, a := range artifacts {
		fmt.Fprintf(&sums, "%s  %s\n", a.SHA256, a.Path)
	}
	return os.WriteFile(filepath.Join(out, "SHA256SUMS"), []byte(sums.String()), 0644)
}

// Standalone packagers historically allow several version directories beneath
// outputs/releases. Their checksum file covers packages, not nested deliveries.
func WritePackageSums(out string) error {
	entries, err := os.ReadDir(out)
	if err != nil {
		return err
	}
	var sums strings.Builder
	for _, entry := range entries {
		ext := filepath.Ext(entry.Name())
		if ext != ".zip" && ext != ".vsix" && ext != ".tgz" && ext != ".whl" && ext != ".jar" && ext != ".pom" {
			continue
		}
		if !entry.Type().IsRegular() || strings.ContainsAny(entry.Name(), "\r\n") {
			return fmt.Errorf("invalid package entry %s", entry.Name())
		}
		file, err := os.Open(filepath.Join(out, entry.Name()))
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, err = io.Copy(hash, file)
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		fmt.Fprintf(&sums, "%x  %s\n", hash.Sum(nil), entry.Name())
	}
	return os.WriteFile(filepath.Join(out, "SHA256SUMS"), []byte(sums.String()), 0644)
}
