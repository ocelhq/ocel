package discovery

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type Language string

const (
	JS     Language = "js"
	Go     Language = "go"
	Python Language = "python"
	Rust   Language = "rust"
)

type Root struct {
	Dir      string
	Language Language
}

const defaultRootName = "infra"

var manifestLanguages = []struct {
	file     string
	language Language
}{
	{"Cargo.toml", Rust},
	{"go.mod", Go},
	{"pyproject.toml", Python},
	{"requirements.txt", Python},
}

func Roots(configDir string, paths []string, appPaths []string) ([]Root, error) {
	dirs, err := rootDirsOf(configDir, paths, appPaths)
	if err != nil {
		return nil, err
	}

	roots := make([]Root, 0, len(dirs))
	for _, dir := range dirs {
		language, err := languageOf(configDir, dir)
		if err != nil {
			return nil, err
		}
		roots = append(roots, Root{Dir: dir, Language: language})
	}
	return roots, nil
}

func rootDirsOf(configDir string, paths []string, appPaths []string) ([]string, error) {
	if paths != nil {
		return configuredRootDirs(configDir, paths)
	}
	return defaultRootDirs(configDir, appPaths), nil
}

func configuredRootDirs(configDir string, paths []string) ([]string, error) {
	var dirs []string
	for _, p := range paths {
		matches, err := resolveRoots(configDir, p)
		if err != nil {
			return nil, err
		}
		if !strings.ContainsAny(p, "*?[") {
			if _, err := os.Stat(matches[0]); err != nil {
				return nil, fmt.Errorf("discovery: the configured path %q does not exist in %s", p, configDir)
			}
		}
		for _, m := range matches {
			if !slices.Contains(dirs, m) {
				dirs = append(dirs, m)
			}
		}
	}
	return dirs, nil
}

func defaultRootDirs(configDir string, appPaths []string) []string {
	candidates := []string{filepath.Join(configDir, defaultRootName)}
	for _, p := range appPaths {
		if p == "" {
			continue
		}
		candidates = append(candidates, filepath.Join(configDir, p, defaultRootName))
	}

	var dirs []string
	for _, dir := range candidates {
		if slices.Contains(dirs, dir) {
			continue
		}
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			continue
		}
		dirs = append(dirs, dir)
	}
	return dirs
}

func languageOf(configDir, dir string) (Language, error) {
	stop, err := filepath.Abs(configDir)
	if err != nil {
		return "", err
	}
	at, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}

	for {
		for _, m := range manifestLanguages {
			if _, err := os.Stat(filepath.Join(at, m.file)); err == nil {
				return m.language, nil
			}
		}
		if at == stop {
			return JS, nil
		}
		parent := filepath.Dir(at)
		if parent == at {
			return JS, nil
		}
		at = parent
	}
}
