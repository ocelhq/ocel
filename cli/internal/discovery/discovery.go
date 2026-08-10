package discovery

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var sourceExtensions = map[string]bool{
	".ts":  true,
	".tsx": true,
	".js":  true,
	".jsx": true,
	".mjs": true,
	".cjs": true,
}

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

func Dirs(configDir string, paths []string) ([]string, error) {
	seen := make(map[string]bool)
	var dirs []string

	for _, p := range paths {
		roots, err := resolveRoots(configDir, p)
		if err != nil {
			return nil, err
		}

		for _, root := range roots {
			found, err := walkDirs(root)
			if err != nil {
				return nil, err
			}
			for _, d := range found {
				if !seen[d] {
					seen[d] = true
					dirs = append(dirs, d)
				}
			}
		}
	}

	sort.Strings(dirs)
	return dirs, nil
}

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
			if path != root && skipDir(d.Name()) {
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

func walkDirs(root string) ([]string, error) {
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return nil, nil
	}

	var dirs []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if path != root && skipDir(d.Name()) {
			return filepath.SkipDir
		}
		abs, absErr := filepath.Abs(path)
		if absErr != nil {
			return absErr
		}
		dirs = append(dirs, abs)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return dirs, nil
}

func skipDir(name string) bool {
	return strings.HasPrefix(name, ".") || name == "node_modules"
}

func isSourceFile(path string) bool {
	return sourceExtensions[filepath.Ext(path)]
}
