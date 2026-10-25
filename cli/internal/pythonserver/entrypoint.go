package pythonserver

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/language"
)

const (
	pythonFileSuffix = ".py"

	fileForm   = "file"
	moduleForm = "module"
)

var pythonIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type Entrypoint struct {
	File   string
	Module string
}

func (e Entrypoint) String() string {
	if e.File != "" {
		return e.File
	}
	return e.Module
}

func (e Entrypoint) arguments() []string {
	if e.File != "" {
		return []string{fileForm, e.File}
	}
	return []string{moduleForm, e.Module}
}

func ResolveEntrypoint(source, entrypoint string) (Entrypoint, error) {
	if entrypoint == "" {
		return Entrypoint{}, fmt.Errorf("%s holds none of %s: set \"entrypoint\" to the file or module that serves it", source, strings.Join(language.PythonEntrypoints, ", "))
	}
	parsed, err := ParseEntrypoint(entrypoint)
	if err != nil {
		return Entrypoint{}, err
	}
	if parsed.File == "" {
		return parsed, nil
	}
	if info, err := os.Stat(filepath.Join(source, filepath.FromSlash(parsed.File))); err != nil || !info.Mode().IsRegular() {
		return Entrypoint{}, fmt.Errorf("entrypoint %q names no file in %s", entrypoint, source)
	}
	return parsed, nil
}

func ParseEntrypoint(entrypoint string) (Entrypoint, error) {
	if strings.Contains(entrypoint, "/") || strings.HasSuffix(entrypoint, pythonFileSuffix) {
		file, err := parseEntryFile(entrypoint)
		if err != nil {
			return Entrypoint{}, err
		}
		return Entrypoint{File: file}, nil
	}
	if err := refuseModuleEntrypoint(entrypoint); err != nil {
		return Entrypoint{}, err
	}
	return Entrypoint{Module: entrypoint}, nil
}

func parseEntryFile(declared string) (string, error) {
	if !strings.HasSuffix(declared, pythonFileSuffix) {
		return "", fmt.Errorf("entrypoint %q is not a .py file: name a .py file in the app's directory, or a module as module[:name]", declared)
	}
	clean := path.Clean(declared)
	if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("entrypoint %q is outside the app's directory", declared)
	}
	return clean, nil
}

func refuseModuleEntrypoint(declared string) error {
	module, name, named := strings.Cut(declared, ":")
	invalid := named && !pythonIdentifier.MatchString(name)
	for _, part := range strings.Split(module, ".") {
		invalid = invalid || !pythonIdentifier.MatchString(part)
	}
	if invalid {
		return fmt.Errorf("entrypoint %q is neither a .py file nor a module path such as my_pkg.api[:name]", declared)
	}
	return nil
}
