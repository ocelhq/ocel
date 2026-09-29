package discovery

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/pkg/constants"
)

type Root struct {
	Dir      string
	Language language.Language
}

var languageExtensions = map[string]language.Language{
	".go":  language.Go,
	".py":  language.Python,
	".rs":  language.Rust,
	".ts":  language.JS,
	".tsx": language.JS,
	".cts": language.JS,
	".js":  language.JS,
	".jsx": language.JS,
	".mjs": language.JS,
	".cjs": language.JS,
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
		roots = append(roots, Root{Dir: dir, Language: language.Rust})
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
		found, err := languageOf(dir)
		if err != nil {
			return nil, err
		}
		if found == "" {
			continue
		}
		if found == language.Rust {
			return nil, fmt.Errorf("discovery: %s contains rust files, and a rust app declares from its own crate: delete the folder and derive ocel::Resources or ocel::Env on a struct in the crate", dir)
		}
		roots = append(roots, Root{Dir: dir, Language: found})
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
	dir := filepath.Join(configDir, constants.DefaultDiscoveryDirName)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return nil
	}
	return []string{dir}
}

func languageOf(dir string) (language.Language, error) {
	var found []language.Language
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
		written, ok := languageExtensions[filepath.Ext(path)]
		if !ok || slices.Contains(found, written) {
			return nil
		}
		found = append(found, written)
		if len(found) > 1 {
			return fmt.Errorf("discovery: %s mixes %s and %s files, and a discovery folder contains one language", dir, found[0], found[1])
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

func walkUp(configDir, dir string, contains func(at string) bool) (string, bool, error) {
	stop, err := filepath.Abs(configDir)
	if err != nil {
		return "", false, err
	}
	at, err := filepath.Abs(dir)
	if err != nil {
		return "", false, err
	}

	for {
		if contains(at) {
			return at, true, nil
		}
		parent := filepath.Dir(at)
		if at == stop || parent == at {
			return stop, false, nil
		}
		at = parent
	}
}
