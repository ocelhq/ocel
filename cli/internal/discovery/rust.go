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

func rustCommands(ctx context.Context, configDir string, roots []Root, server Server) ([]*exec.Cmd, error) {
	commands := make([]*exec.Cmd, 0, len(roots))
	for _, root := range roots {
		cmd, err := rustCommand(ctx, configDir, root, server)
		if err != nil {
			return nil, err
		}
		commands = append(commands, cmd)
	}
	return commands, nil
}

func rustCommand(ctx context.Context, _ string, root Root, server Server) (*exec.Cmd, error) {
	cmd, workspaceRoot, err := cargoRunCommand(ctx, root)
	if err != nil {
		return nil, err
	}
	cmd.Env = append(append(os.Environ(), server.Env()...), "OCEL_SOURCE_ROOT="+workspaceRoot)
	return cmd, nil
}

func cargoRunCommand(ctx context.Context, root Root) (*exec.Cmd, string, error) {
	workspace, err := cargo.Metadata(ctx, root.Dir, "--no-deps")
	if err != nil {
		return nil, "", fmt.Errorf("discovery: %w", err)
	}
	crate, ok := workspace.PackageAt(root.Dir)
	if !ok {
		return nil, "", fmt.Errorf("discovery: %s has no Cargo.toml naming a package to declare from", root.Dir)
	}
	bin, err := crate.FindBin(root.Bin)
	if err != nil {
		return nil, "", fmt.Errorf("discovery: resources are declared by running one binary of the crate at %s: %w", root.Dir, err)
	}
	if err := refuseDivergentDefaultBin(root, crate, bin); err != nil {
		return nil, "", err
	}

	cmd := exec.Command("cargo", "run", "--quiet", "--manifest-path", crate.ManifestPath, "--bin", bin.Name)
	cmd.Dir = workspace.Root
	return cmd, workspace.Root, nil
}

func refuseDivergentDefaultBin(root Root, crate cargo.Package, bin cargo.Target) error {
	if root.Bin == "" || root.DefaultBinApp == "" {
		return nil
	}
	runs, err := crate.FindBin("")
	if err == nil && runs.Name == bin.Name {
		return nil
	}
	ran := "no one binary"
	if err == nil {
		ran = fmt.Sprintf("%q", runs.Name)
	}
	return fmt.Errorf("discovery: apps %q and %q are both built from the crate at %s: %q names the binary %q, and %q names none, so it runs %s: resources are declared by running one binary of a crate, so give both apps the same \"entrypoint\"", root.BinApp, root.DefaultBinApp, root.Dir, root.BinApp, bin.Name, root.DefaultBinApp, ran)
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
