package discovery

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/workdir"

	"github.com/ocelhq/ocel/cli/internal/gomodule"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/tracing"
)

func goCommands(ctx context.Context, configDir string, roots []Root, server Server) ([]*tracing.Cmd, error) {
	var modules []string
	imports := map[string][]string{}
	for _, root := range roots {
		moduleRoot, pkg, err := goPackage(configDir, root)
		if err != nil {
			return nil, err
		}
		if _, seen := imports[moduleRoot]; !seen {
			modules = append(modules, moduleRoot)
		}
		imports[moduleRoot] = append(imports[moduleRoot], pkg)
	}

	commands := make([]*tracing.Cmd, 0, len(modules))
	for _, moduleRoot := range modules {
		var source strings.Builder
		source.WriteString("package main\n\nimport (\n")
		for _, pkg := range imports[moduleRoot] {
			fmt.Fprintf(&source, "\t_ %q\n", pkg)
		}
		source.WriteString(")\n\nfunc main() {}\n")
		pkg, err := writeGoEntry(moduleRoot, "discovery", source.String())
		if err != nil {
			return nil, err
		}

		cmd := tracing.Command(ctx, "go", "run", "-trimpath=false", pkg)
		cmd.Dir = moduleRoot
		cmd.Env = slices.Concat(os.Environ(), server.Env())
		commands = append(commands, cmd)
	}
	return commands, nil
}

func goPackage(configDir string, root Root) (string, string, error) {
	moduleRoot, modulePath, err := goModule(configDir, root.Dir)
	if err != nil {
		return "", "", err
	}
	rel, err := filepath.Rel(moduleRoot, root.Dir)
	if err != nil {
		return "", "", fmt.Errorf("discovery: %s is not inside the module at %s", root.Dir, moduleRoot)
	}

	pkg := modulePath
	if rel != "." {
		pkg += "/" + filepath.ToSlash(rel)
	}
	return moduleRoot, pkg, nil
}

func writeGoEntry(moduleRoot, role, source string) (string, error) {
	entry, err := workdir.WriteShared(filepath.Join(moduleRoot, buildoutput.StateDir, role), "main.go", []byte(source))
	if err != nil {
		return "", fmt.Errorf("discovery: %w", err)
	}
	rel, err := filepath.Rel(moduleRoot, filepath.Dir(entry))
	if err != nil {
		return "", fmt.Errorf("discovery: %w", err)
	}
	return "./" + filepath.ToSlash(rel), nil
}

func goModule(configDir, dir string) (string, string, error) {
	module, found, err := gomodule.FindInProject(dir, configDir)
	if err != nil {
		return "", "", fmt.Errorf("discovery: %w", err)
	}
	if !found {
		return "", "", fmt.Errorf("discovery: %s is a go folder, but neither it nor any folder up to %s has a %s", dir, configDir, gomodule.File)
	}
	if module.Path == "" {
		return "", "", fmt.Errorf("discovery: the %s at %s names no module", gomodule.File, module.Dir)
	}
	return module.Dir, module.Path, nil
}
