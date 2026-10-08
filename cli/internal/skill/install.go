package skill

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	Name         = "ocel"
	manifestName = "SKILL.md"
	versionKey   = "ocel-version"
	fence        = "---\n"
)

func Dirs(base string) []string {
	return []string{
		filepath.Join(base, ".claude", "skills", Name),
		filepath.Join(base, ".agents", "skills", Name),
	}
}

func IsInstalled(root string) bool {
	for _, dir := range Dirs(root) {
		if info, err := os.Stat(filepath.Join(dir, manifestName)); err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	return false
}

func ListFiles() []string {
	var names []string
	err := fs.WalkDir(Files(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			names = append(names, name)
		}
		return err
	})
	if err != nil {
		panic(err)
	}
	return names
}

func Write(dir, version string) error {
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("clear %s: %w", dir, err)
	}
	for _, name := range ListFiles() {
		content, err := fs.ReadFile(Files(), name)
		if err != nil {
			return err
		}
		if name == manifestName {
			if content, err = stamp(content, version); err != nil {
				return err
			}
		}
		out := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(out), err)
		}
		if err := os.WriteFile(out, content, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", out, err)
		}
	}
	return nil
}

func ReadVersion(dir string) (string, error) {
	content, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		return "", err
	}
	front, _, err := splitManifest(content)
	if err != nil {
		return "", fmt.Errorf("%s: %w", filepath.Join(dir, manifestName), err)
	}
	var parsed struct {
		Metadata map[string]string `yaml:"metadata"`
	}
	if err := yaml.Unmarshal([]byte(front), &parsed); err != nil {
		return "", fmt.Errorf("%s: %w", filepath.Join(dir, manifestName), err)
	}
	return parsed.Metadata[versionKey], nil
}

func stamp(content []byte, version string) ([]byte, error) {
	front, body, err := splitManifest(content)
	if err != nil {
		return nil, err
	}
	entry := "  " + versionKey + ": " + strconv.Quote(version) + "\n"
	lines := strings.SplitAfter(front, "\n")
	stamped := ""
	inserted := false
	for _, line := range lines {
		stamped += line
		if !inserted && strings.TrimRight(line, " \n") == "metadata:" {
			stamped += entry
			inserted = true
		}
	}
	if !inserted {
		stamped += "metadata:\n" + entry
	}
	return []byte(fence + stamped + fence + body), nil
}

func splitManifest(content []byte) (front, body string, err error) {
	rest, opened := strings.CutPrefix(string(content), fence)
	if !opened {
		return "", "", errors.New(manifestName + " opens with no frontmatter")
	}
	if closing, found := strings.CutPrefix(rest, fence); found {
		return "", closing, nil
	}
	front, body, closed := strings.Cut(rest, "\n"+fence)
	if !closed {
		return "", "", errors.New(manifestName + " never closes its frontmatter")
	}
	return front + "\n", body, nil
}
