package discovery

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"

	"github.com/ocelhq/ocel/cli/internal/cargo"
)

const ocelCrate = "ocel-sdk"

type rustLauncher struct{}

func (rustLauncher) Command(ctx context.Context, _ string, root Root, server Server) (*exec.Cmd, error) {
	workspace, err := cargo.Metadata(ctx, root.Dir, "--no-deps")
	if err != nil {
		return nil, fmt.Errorf("discovery: %w", err)
	}
	crate, ok := workspace.PackageAt(root.Dir)
	if !ok {
		return nil, fmt.Errorf("discovery: %s has no Cargo.toml naming a package to declare from", root.Dir)
	}
	bins := crate.Bins()
	if len(bins) == 0 {
		return nil, fmt.Errorf("discovery: %s contains %s, which builds no binary to declare from", root.Dir, crate.Name)
	}
	if len(bins) > 1 {
		return nil, fmt.Errorf("discovery: %s builds %d binaries, and ocel runs one binary per crate: keep one bin target in the crate at %s", crate.Name, len(bins), root.Dir)
	}

	cmd := exec.CommandContext(ctx, "cargo", "run", "--quiet", "--manifest-path", crate.ManifestPath, "--bin", bins[0].Name)
	cmd.Dir = workspace.Root
	cmd.Env = append(append(os.Environ(), server.Env()...), "OCEL_SOURCE_ROOT="+workspace.Root)
	return cmd, nil
}

func declaresThroughOcel(at string) bool {
	manifest, err := os.ReadFile(filepath.Join(at, "Cargo.toml"))
	if err != nil {
		return false
	}
	var crate struct {
		Dependencies map[string]any `toml:"dependencies"`
		Target       map[string]struct {
			Dependencies map[string]any `toml:"dependencies"`
		} `toml:"target"`
	}
	if err := toml.Unmarshal(manifest, &crate); err != nil {
		return false
	}
	if namesOcel(crate.Dependencies) {
		return true
	}
	for _, target := range crate.Target {
		if namesOcel(target.Dependencies) {
			return true
		}
	}
	return false
}

func namesOcel(dependencies map[string]any) bool {
	for name, spec := range dependencies {
		table, _ := spec.(map[string]any)
		if renamed, ok := table["package"].(string); ok {
			if renamed == ocelCrate {
				return true
			}
			continue
		}
		if name == ocelCrate {
			return true
		}
	}
	return false
}
