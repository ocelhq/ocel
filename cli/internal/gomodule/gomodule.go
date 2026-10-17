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

func FindEnclosing(dir string, isLast func(dir string) bool) (Module, bool, error) {
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
