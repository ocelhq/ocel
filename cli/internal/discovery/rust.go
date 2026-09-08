package discovery

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/ocelhq/ocel/cli/internal/cargo"
)

type rustLauncher struct{}

func (rustLauncher) Command(ctx context.Context, configDir string, root Root, serverURL string) (*exec.Cmd, error) {
	crateDir, found, err := walkUp(configDir, root.Dir, holdsACrate)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("discovery: %s is a rust folder, but no Cargo.toml stands between it and %s", root.Dir, configDir)
	}

	workspace, err := cargo.Metadata(ctx, crateDir, "--no-deps")
	if err != nil {
		return nil, fmt.Errorf("discovery: %w", err)
	}
	crate, ok := workspace.PackageAt(crateDir)
	if !ok {
		return nil, fmt.Errorf("discovery: the Cargo.toml at %s names no package to run %s from", crateDir, root.Dir)
	}
	bins := crate.Bins()
	if len(bins) == 0 {
		return nil, fmt.Errorf("discovery: %s declares resources but %s builds no binary to run them from", root.Dir, crate.Name)
	}

	cmd := exec.CommandContext(ctx, "cargo", "run", "--quiet", "--manifest-path", crate.ManifestPath, "--bin", bins[0].Name)
	cmd.Dir = workspace.Root
	cmd.Env = append(os.Environ(), "OCEL_PHASE=discovery", "OCEL_DEV_SERVER="+serverURL, "OCEL_SOURCE_ROOT="+workspace.Root)
	return cmd, nil
}

func holdsACrate(at string) bool {
	info, err := os.Stat(filepath.Join(at, "Cargo.toml"))
	return err == nil && info.Mode().IsRegular()
}
