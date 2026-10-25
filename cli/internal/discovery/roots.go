package discovery

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/cli/internal/project"
)

type Root struct {
	Dir           string
	Language      language.Language
	Bin           string
	BinApp        string
	DefaultBinApp string
}

var languageExtensions = map[string]language.Language{
	".go":  language.Go,
	".py":  language.Python,
	".rs":  language.Rust,
	".ts":  language.JS,
	".tsx": language.JS,
	".cts": language.JS,
	".mts": language.JS,
	".js":  language.JS,
	".jsx": language.JS,
	".mjs": language.JS,
	".cjs": language.JS,
}

func RootsOf(cfg *project.Project) ([]Root, error) {
	roots, err := Roots(cfg.Dir, cfg.DiscoveryPaths)
	if err != nil {
		return nil, err
	}
	crates, err := crateRoots(cfg)
	if err != nil {
		return nil, err
	}
	return append(roots, crates...), nil
}

func crateRoots(cfg *project.Project) ([]Root, error) {
	type crate struct {
		dir string
		app *project.App
	}
	crates := []crate{{dir: filepath.Clean(cfg.Dir)}}
	for i := range cfg.Apps {
		crates = append(crates, crate{dir: filepath.Join(cfg.Dir, cfg.Apps[i].Path), app: &cfg.Apps[i]})
	}

	var roots []Root
	for _, c := range crates {
		if !declaresThroughOcel(c.dir) {
			continue
		}
		at := slices.IndexFunc(roots, func(r Root) bool { return r.Dir == c.dir })
		if at < 0 {
			roots = append(roots, Root{Dir: c.dir, Language: language.Rust})
			at = len(roots) - 1
		}
		root := &roots[at]
		switch {
		case c.app == nil || c.app.Language != language.Rust:
		case c.app.Entrypoint == "":
			if root.DefaultBinApp == "" {
				root.DefaultBinApp = c.app.Name
			}
		case root.Bin != "" && root.Bin != c.app.Entrypoint:
			return nil, fmt.Errorf("discovery: apps %q and %q are both built from the crate at %s, and name the binaries %q and %q: give both apps the same \"entrypoint\"", root.BinApp, c.app.Name, c.dir, root.Bin, c.app.Entrypoint)
		default:
			root.Bin, root.BinApp = c.app.Entrypoint, c.app.Name
		}
	}
	return roots, nil
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
			return nil, fmt.Errorf("discovery: %s contains rust files: delete the folder and derive ocel::Resources or ocel::Env on a struct in the app's crate", dir)
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

const DefaultRootDirName = "infra"

func defaultRootDirs(configDir string) []string {
	dir := filepath.Join(configDir, DefaultRootDirName)
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
			if path != dir && IsSkippedDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		written, ok := languageExtensions[filepath.Ext(path)]
		if !ok || isTestFile(path) || slices.Contains(found, written) {
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
