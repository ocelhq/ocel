// Package discovery locates resource-declaration source files under a
// project's configured discovery.paths and bundles them into a single
// side-effect-import entrypoint that self-registers each resource with the
// local dev server.
package discovery

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// sourceExtensions are the file types imported for their side effects.
var sourceExtensions = map[string]bool{
	".ts":  true,
	".tsx": true,
	".js":  true,
	".jsx": true,
	".mjs": true,
	".cjs": true,
}

// Discover resolves paths (each relative to configDir, optionally containing
// glob metacharacters for monorepo layouts like "packages/*/ocel") and
// returns the absolute path of every source file found under them, sorted
// and de-duplicated. A path that matches nothing is silently skipped — an
// unconfigured discovery directory is not an error.
func Discover(configDir string, paths []string) ([]string, error) {
	seen := make(map[string]bool)
	var files []string

	for _, p := range paths {
		roots, err := resolveRoots(configDir, p)
		if err != nil {
			return nil, err
		}

		for _, root := range roots {
			found, err := walkSourceFiles(root)
			if err != nil {
				return nil, err
			}
			for _, f := range found {
				if !seen[f] {
					seen[f] = true
					files = append(files, f)
				}
			}
		}
	}

	sort.Strings(files)
	return files, nil
}

// resolveRoots expands a single discovery.paths entry into the concrete
// directories (or files) it refers to.
func resolveRoots(configDir, pattern string) ([]string, error) {
	joined := filepath.Join(configDir, pattern)

	if !strings.ContainsAny(pattern, "*?[") {
		return []string{joined}, nil
	}

	matches, err := filepath.Glob(joined)
	if err != nil {
		return nil, err
	}
	return matches, nil
}

// walkSourceFiles returns every source file under root (root itself if it's
// a file), skipping node_modules and dotfiles/dotdirs. Missing roots yield
// no files and no error.
func walkSourceFiles(root string) ([]string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, nil
	}

	if !info.IsDir() {
		if isSourceFile(root) {
			abs, err := filepath.Abs(root)
			if err != nil {
				return nil, err
			}
			return []string{abs}, nil
		}
		return nil, nil
	}

	var files []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (strings.HasPrefix(name, ".") || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if isSourceFile(path) {
			abs, err := filepath.Abs(path)
			if err != nil {
				return err
			}
			files = append(files, abs)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

func isSourceFile(path string) bool {
	return sourceExtensions[filepath.Ext(path)]
}
