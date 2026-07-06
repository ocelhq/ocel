// Package projectconfig locates, transpiles, and executes a user's
// ocel.config.ts to resolve their project's configuration.
package projectconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

// ConfigFileName is the name of the file Resolve looks for.
const ConfigFileName = "ocel.config.ts"

// buildDirName is the Ocel-internal build-artifact folder written next to
// the resolved config. It must stay gitignored.
const buildDirName = ".ocel"

// initHint is appended to every resolution failure so the user knows how to
// fix it.
const initHint = "run `ocel init` to create one"

// defaultDiscoveryPaths is used for discovery.paths when the config omits it.
var defaultDiscoveryPaths = []string{"ocel"}

// Discovery controls where the CLI looks for resource declarations.
type Discovery struct {
	Paths []string
}

// Config is the resolved, defaulted project configuration read from
// ocel.config.ts.
type Config struct {
	ProjectID string
	Discovery Discovery
}

// rawConfig mirrors the JSON shape emitted by executing the user's bundled
// ocel.config.ts.
type rawConfig struct {
	ProjectID string `json:"projectId"`
	Discovery struct {
		Paths []string `json:"paths"`
	} `json:"discovery"`
}

// Resolve walks up from startDir to find the nearest ancestor
// ocel.config.ts, bundles and executes it, and returns its parsed,
// defaulted configuration.
//
// If no config is found, it can't be bundled/executed, or it doesn't emit a
// projectId, the returned error's message instructs the user to run
// `ocel init`.
func Resolve(startDir string) (*Config, error) {
	configPath, err := findConfigFile(startDir)
	if err != nil {
		return nil, fmt.Errorf("no %s found in %s or any parent directory — %s", ConfigFileName, startDir, initHint)
	}

	output, err := buildAndRun(configPath)
	if err != nil {
		return nil, fmt.Errorf("could not read %s: %w — %s", configPath, err, initHint)
	}

	var raw rawConfig
	if err := json.Unmarshal(output, &raw); err != nil {
		return nil, fmt.Errorf("%s did not emit valid configuration: %w — %s", configPath, err, initHint)
	}
	if raw.ProjectID == "" {
		return nil, fmt.Errorf("%s is missing required \"projectId\" — %s", configPath, initHint)
	}

	paths := raw.Discovery.Paths
	if len(paths) == 0 {
		paths = defaultDiscoveryPaths
	}

	return &Config{
		ProjectID: raw.ProjectID,
		Discovery: Discovery{Paths: paths},
	}, nil
}

// buildAndRun bundles configPath (and a small wrapper that JSON-serializes
// its default export) with esbuild's Go API, writes the result under
// .ocel/ next to the config, executes it with the user's node, and returns
// what it wrote to stdout.
func buildAndRun(configPath string) ([]byte, error) {
	dir := filepath.Dir(configPath)
	outDir := filepath.Join(dir, buildDirName)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", buildDirName, err)
	}
	outfile := filepath.Join(outDir, "config.mjs")

	entry := fmt.Sprintf("import config from %q;\nprocess.stdout.write(JSON.stringify(config));\n", configPath)

	result := api.Build(api.BuildOptions{
		Stdin: &api.StdinOptions{
			Contents:   entry,
			ResolveDir: dir,
			Sourcefile: "ocel-config-entry.ts",
			Loader:     api.LoaderTS,
		},
		Bundle:   true,
		Platform: api.PlatformNode,
		Format:   api.FormatESModule,
		Outfile:  outfile,
		Write:    true,
	})
	if len(result.Errors) > 0 {
		msgs := api.FormatMessages(result.Errors, api.FormatMessagesOptions{Color: false})
		return nil, fmt.Errorf("bundle failed:\n%s", strings.Join(msgs, "\n"))
	}

	if _, err := exec.LookPath("node"); err != nil {
		return nil, fmt.Errorf("node not found on PATH: %w", err)
	}

	cmd := exec.Command("node", outfile)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		if stderr.Len() > 0 {
			return nil, fmt.Errorf("node exited with error: %s", strings.TrimSpace(stderr.String()))
		}
		return nil, fmt.Errorf("run node: %w", err)
	}

	return stdout, nil
}

// findConfigFile walks up from startDir (tsconfig-style) looking for the
// nearest ancestor ConfigFileName. It returns os.ErrNotExist if none is
// found by the time it reaches the filesystem root.
func findConfigFile(startDir string) (string, error) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", err
	}

	for {
		candidate := filepath.Join(dir, ConfigFileName)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}
