package discovery

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
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
	{"package.json", JS},
}

var languageExtensions = map[string]Language{
	".go":  Go,
	".py":  Python,
	".rs":  Rust,
	".ts":  JS,
	".tsx": JS,
	".js":  JS,
	".jsx": JS,
	".mjs": JS,
	".cjs": JS,
}

func RootsOf(cfg *projectconfig.Config) ([]Root, error) {
	roots, err := Roots(cfg.Dir, cfg.Discovery.Paths)
	if err != nil {
		return nil, err
	}
	return append(roots, crateRoots(cfg)...), nil
}

func crateRoots(cfg *projectconfig.Config) []Root {
	dirs := []string{filepath.Clean(cfg.Dir)}
	for _, app := range cfg.Apps {
		dirs = append(dirs, filepath.Join(cfg.Dir, app.Path))
	}

	var roots []Root
	for _, dir := range dirs {
		if !declaresThroughOcel(dir) {
			continue
		}
		if slices.ContainsFunc(roots, func(r Root) bool { return r.Dir == dir }) {
			continue
		}
		roots = append(roots, Root{Dir: dir, Language: Rust})
	}
	return roots
}

func Roots(configDir string, paths []string) ([]Root, error) {
	dirs, err := rootDirsOf(configDir, paths)
	if err != nil {
		return nil, err
	}

	roots := make([]Root, 0, len(dirs))
	for _, dir := range dirs {
		language, err := languageOf(dir)
		if err != nil {
			return nil, err
		}
		if language == "" {
			continue
		}
		if language == Rust {
			return nil, fmt.Errorf("discovery: %s holds rust files, and a rust app declares from its own crate: delete the folder and derive ocel::Resources or ocel::Env on a struct in the crate", dir)
		}
		roots = append(roots, Root{Dir: dir, Language: language})
	}
	return roots, nil
}

func rootDirsOf(configDir string, paths []string) ([]string, error) {
	if paths != nil {
		return configuredRootDirs(configDir, paths)
	}
	return defaultRootDirs(configDir), nil
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

func defaultRootDirs(configDir string) []string {
	dir := filepath.Join(configDir, defaultRootName)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return nil
	}
	return []string{dir}
}

func HoldsJS(cfg *projectconfig.Config) (bool, error) {
	if _, err := os.Stat(filepath.Join(cfg.Dir, "package.json")); err == nil {
		return true, nil
	}
	roots, err := RootsOf(cfg)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(roots, func(root Root) bool { return root.Language == JS }), nil
}

func LanguageOfApp(dir string) Language {
	for _, m := range manifestLanguages {
		if _, err := os.Stat(filepath.Join(dir, m.file)); err == nil {
			return m.language
		}
	}
	return JS
}

func languageOf(dir string) (Language, error) {
	var found []Language
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != dir && skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		language, ok := languageExtensions[filepath.Ext(path)]
		if !ok || slices.Contains(found, language) {
			return nil
		}
		found = append(found, language)
		if len(found) > 1 {
			return fmt.Errorf("discovery: %s mixes %s and %s files, and a discovery folder holds one language", dir, found[0], found[1])
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(found) == 0 {
		return "", nil
	}
	return found[0], nil
}

func walkUp(configDir, dir string, holds func(at string) bool) (string, bool, error) {
	stop, err := filepath.Abs(configDir)
	if err != nil {
		return "", false, err
	}
	at, err := filepath.Abs(dir)
	if err != nil {
		return "", false, err
	}

	for {
		if holds(at) {
			return at, true, nil
		}
		parent := filepath.Dir(at)
		if at == stop || parent == at {
			return stop, false, nil
		}
		at = parent
	}
}
