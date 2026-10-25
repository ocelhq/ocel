package gomodule

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/mod/modfile"
)

const File = "go.mod"

type Module struct {
	Dir             string
	Path            string
	GoVersion       string
	ReplacementDirs []string
}

func findEnclosing(dir string, isLast func(dir string) bool) (Module, bool, error) {
	at, err := filepath.Abs(dir)
	if err != nil {
		return Module{}, false, err
	}
	for {
		named := filepath.Join(at, File)
		read, err := os.ReadFile(named)
		if err == nil {
			module, err := parse(at, named, read)
			return module, err == nil, err
		}
		if !os.IsNotExist(err) {
			return Module{}, false, fmt.Errorf("read %s: %w", named, err)
		}
		parent := filepath.Dir(at)
		if isLast(at) || parent == at {
			return Module{}, false, nil
		}
		at = parent
	}
}

func FindInProject(dir, projectDir string) (Module, bool, error) {
	root, err := filepath.Abs(projectDir)
	if err != nil {
		return Module{}, false, err
	}
	return findEnclosing(dir, func(at string) bool { return at == root || !within(root, at) })
}

func PackagePath(moduleDir, appDir, entrypoint string) (string, error) {
	dir := filepath.Join(appDir, filepath.FromSlash(entrypoint))
	rel, err := filepath.Rel(moduleDir, dir)
	if err != nil || !within(moduleDir, dir) {
		return "", fmt.Errorf("entrypoint %q of the go app in %s names %s, outside the module rooted at %s", entrypoint, appDir, dir, moduleDir)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return "", fmt.Errorf("entrypoint %q of the go app in %s names no directory there: give it the main package's directory, not a .go file", entrypoint, appDir)
	}
	if rel == "." {
		return ".", nil
	}
	return "./" + filepath.ToSlash(rel), nil
}

func within(root, dir string) bool {
	rel, err := filepath.Rel(root, dir)
	return err == nil && filepath.IsLocal(rel)
}

func parse(dir, named string, read []byte) (Module, error) {
	parsed, err := modfile.Parse(named, read, nil)
	if err != nil {
		return Module{}, fmt.Errorf("read %s: %w", named, err)
	}
	module := Module{Dir: dir}
	if parsed.Module != nil {
		module.Path = parsed.Module.Mod.Path
	}
	if parsed.Go != nil {
		module.GoVersion = parsed.Go.Version
	}
	for _, replace := range parsed.Replace {
		if replace.New.Version != "" || !modfile.IsDirectoryPath(replace.New.Path) {
			continue
		}
		replaced := filepath.FromSlash(replace.New.Path)
		if !filepath.IsAbs(replaced) {
			replaced = filepath.Join(dir, replaced)
		}
		module.ReplacementDirs = append(module.ReplacementDirs, filepath.Clean(replaced))
	}
	return module, nil
}
