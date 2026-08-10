package providerlocator

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

//go:embed resolve-provider.cjs
var resolverScript []byte

const scratchDirName = ".ocel"

func Locate(projectDir, packageName string) (string, error) {
	if _, err := exec.LookPath("node"); err != nil {
		return "", fmt.Errorf("node not found on PATH: %w", err)
	}

	scratchDir := filepath.Join(projectDir, scratchDirName)
	if err := os.MkdirAll(scratchDir, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", scratchDirName, err)
	}

	scriptPath := filepath.Join(scratchDir, "resolve-provider.cjs")
	if err := os.WriteFile(scriptPath, resolverScript, 0o644); err != nil {
		return "", fmt.Errorf("write resolver script: %w", err)
	}

	cmd := exec.Command("node", scriptPath, packageName)
	cmd.Dir = projectDir
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s", msg)
	}

	path := strings.TrimSpace(stdout.String())
	if path == "" {
		return "", fmt.Errorf("locate provider binary for %s: resolver returned no path", packageName)
	}
	return path, nil
}
