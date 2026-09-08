package discovery

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
)

const goEntryDir = ".ocel/discovery"

type goLauncher struct{}

func (goLauncher) Command(ctx context.Context, configDir string, root Root, serverURL string) (*exec.Cmd, error) {
	moduleRoot, modulePath, err := goModule(configDir, root.Dir)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(moduleRoot, root.Dir)
	if err != nil {
		return nil, fmt.Errorf("discovery: %s is not inside the module at %s", root.Dir, moduleRoot)
	}

	pkg := modulePath
	if rel != "." {
		pkg += "/" + filepath.ToSlash(rel)
	}
	entry := filepath.Join(moduleRoot, filepath.FromSlash(goEntryDir), "main.go")
	if err := os.MkdirAll(filepath.Dir(entry), 0o755); err != nil {
		return nil, fmt.Errorf("discovery: %w", err)
	}
	source := fmt.Sprintf("package main\n\nimport _ %q\n\nfunc main() {}\n", pkg)
	if err := os.WriteFile(entry, []byte(source), 0o644); err != nil {
		return nil, fmt.Errorf("discovery: %w", err)
	}

	cmd := exec.CommandContext(ctx, "go", "run", "./"+goEntryDir)
	cmd.Dir = moduleRoot
	cmd.Env = append(os.Environ(), "OCEL_PHASE=discovery", "OCEL_DEV_SERVER="+serverURL)
	return cmd, nil
}

var goModulePathRE = regexp.MustCompile(`(?m)^\s*module\s+(\S+)`)

func goModule(configDir, dir string) (string, string, error) {
	stop, err := filepath.Abs(configDir)
	if err != nil {
		return "", "", err
	}
	at, err := filepath.Abs(dir)
	if err != nil {
		return "", "", err
	}

	for {
		contents, err := os.ReadFile(filepath.Join(at, "go.mod"))
		if err == nil {
			match := goModulePathRE.FindSubmatch(contents)
			if match == nil {
				return "", "", fmt.Errorf("discovery: the go.mod at %s names no module", at)
			}
			return at, string(match[1]), nil
		}
		parent := filepath.Dir(at)
		if at == stop || parent == at {
			return "", "", fmt.Errorf("discovery: %s is a go folder, but no go.mod stands between it and %s", dir, configDir)
		}
		at = parent
	}
}
