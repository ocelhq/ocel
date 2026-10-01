package discovery

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"

	"github.com/ocelhq/ocel/pkg/statedir"
)

const goEntryDir = statedir.Name + "/discovery"

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

var goModulePathRE = regexp.MustCompile(`(?m)^\s*module\s+(\S+)`)

func goModule(configDir, dir string) (string, string, error) {
	var declaration []byte
	moduleRoot, found, err := walkUp(configDir, dir, func(at string) bool {
		contents, err := os.ReadFile(filepath.Join(at, "go.mod"))
		if err != nil {
			return false
		}
		declaration = contents
		return true
	})
	if err != nil {
		return "", "", err
	}
	if !found {
		return "", "", fmt.Errorf("discovery: %s is a go folder, but neither it nor any folder up to %s has a go.mod", dir, configDir)
	}

	match := goModulePathRE.FindSubmatch(declaration)
	if match == nil {
		return "", "", fmt.Errorf("discovery: the go.mod at %s names no module", moduleRoot)
	}
	return moduleRoot, string(match[1]), nil
}
