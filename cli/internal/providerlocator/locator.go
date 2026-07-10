// Package providerlocator resolves a provider descriptor's package name
// (e.g. "@ocel/provider-aws", see cli/internal/projectconfig.ProviderDescriptor)
// to the absolute path of the concrete binary for the host platform.
//
// Resolution is delegated to Node's own require.resolve rather than
// reimplemented in Go, mirroring packages/ocel/bin/run.js: it maps the host
// platform to a `<package>-<platform>-<arch>` package (e.g.
// "@ocel/provider-aws-linux-x64"), the binary distribution convention every
// `@ocel/provider-*` package follows, and lets Node's own module resolution
// find it in whatever layout npm/pnpm/yarn actually installed. This reuses
// the package manager's resolution logic instead of reimplementing
// pnpm's node_modules layout rules in Go.
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

// scratchDirName is the same Ocel-internal build-artifact folder
// projectconfig.buildAndRun writes its bundled config to. Writing the
// resolver script here too — inside the project directory — means Node
// walks that project's own node_modules when resolving, not wherever this
// process happens to be running from.
const scratchDirName = ".ocel"

// Locate resolves packageName's platform binary for the project rooted at
// projectDir (typically projectconfig.Config.Dir), returning its absolute
// path. It returns a clear, actionable error if Node isn't available or the
// platform package isn't installed.
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
