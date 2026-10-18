package discovery

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/ocelhq/ocel/cli/internal/gomodule"
	"github.com/ocelhq/ocel/pkg/buildoutput"
)

const goEntryDir = buildoutput.StateDir + "/discovery"

func goCommand(ctx context.Context, configDir string, root Root, server Server) (*exec.Cmd, error) {
	moduleRoot, pkg, err := goPackage(configDir, root)
	if err != nil {
		return nil, err
	}
	source := fmt.Sprintf("package main\n\nimport _ %q\n\nfunc main() {}\n", pkg)
	if err := writeGoEntry(moduleRoot, goEntryDir, source); err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, "go", "run", "-trimpath=false", "./"+goEntryDir)
	cmd.Dir = moduleRoot
	cmd.Env = append(os.Environ(), server.Env()...)
	return cmd, nil
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

func writeGoEntry(moduleRoot, dir, source string) error {
	entry := filepath.Join(moduleRoot, filepath.FromSlash(dir), "main.go")
	if err := os.MkdirAll(filepath.Dir(entry), 0o755); err != nil {
		return fmt.Errorf("discovery: %w", err)
	}
	if err := os.WriteFile(entry, []byte(source), 0o644); err != nil {
		return fmt.Errorf("discovery: %w", err)
	}
	return nil
}

func goModule(configDir, dir string) (string, string, error) {
	stop, err := filepath.Abs(configDir)
	if err != nil {
		return "", "", err
	}
	module, found, err := gomodule.FindEnclosing(dir, func(at string) bool { return at == stop })
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
