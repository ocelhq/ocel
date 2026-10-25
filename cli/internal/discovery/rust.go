package discovery

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"

	"github.com/ocelhq/ocel/cli/internal/cargo"
)

const ocelCrate = "ocel-sdk"

const ocelMainMarker = "ocel-main-marker-v1:a97e2b52f933853f5f606937"

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
	binary, err := findRustBinary(ctx, root)
	if err != nil {
		return nil, "", err
	}
	if err := refuseABinaryWithoutOcelMain(ctx, binary); err != nil {
		return nil, "", err
	}
	cmd := exec.Command("cargo", "run", "--quiet", "--manifest-path", binary.crate.ManifestPath, "--bin", binary.bin.Name)
	cmd.Dir = binary.workspace.Root
	return cmd, binary.workspace.Root, nil
}

type rustBinary struct {
	workspace cargo.Workspace
	crate     cargo.Package
	bin       cargo.Target
}

func findRustBinary(ctx context.Context, root Root) (rustBinary, error) {
	workspace, err := cargo.Metadata(ctx, root.Dir, "--no-deps")
	if err != nil {
		return rustBinary{}, fmt.Errorf("discovery: %w", err)
	}
	crate, ok := workspace.PackageAt(root.Dir)
	if !ok {
		return rustBinary{}, fmt.Errorf("discovery: %s has no Cargo.toml naming a package to declare from", root.Dir)
	}
	bin, err := crate.FindBin(root.Bin)
	if err != nil {
		return rustBinary{}, fmt.Errorf("discovery: resources are declared by running one binary of the crate at %s: %w", root.Dir, err)
	}
	if err := refuseDivergentDefaultBin(root, crate, bin); err != nil {
		return rustBinary{}, err
	}
	return rustBinary{workspace: workspace, crate: crate, bin: bin}, nil
}

func refuseABinaryWithoutOcelMain(ctx context.Context, binary rustBinary) error {
	executable, err := cargo.Build{Crate: binary.crate, Bin: binary.bin.Name, Dir: binary.workspace.Root}.Run(ctx)
	if err != nil {
		return fmt.Errorf("discovery: %w", err)
	}
	marked, err := hasOcelMainMarker(executable)
	if err != nil {
		return fmt.Errorf("discovery: %w", err)
	}
	if !marked {
		return fmt.Errorf("discovery: the binary %q of the crate at %s has no #[ocel::main] and no call to ocel::discover(), or was built with an older ocel-sdk: put #[ocel::main] on fn main in %s", binary.bin.Name, binary.crate.Dir(), binary.bin.SrcPath)
	}
	return nil
}

func hasOcelMainMarker(executable string) (bool, error) {
	file, err := os.Open(executable)
	if err != nil {
		return false, err
	}
	defer file.Close()
	marker := []byte(ocelMainMarker)
	buf := make([]byte, 1<<20)
	kept := 0
	for {
		n, err := file.Read(buf[kept:])
		read := buf[:kept+n]
		if bytes.Contains(read, marker) {
			return true, nil
		}
		if errors.Is(err, io.EOF) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		kept = min(len(marker)-1, len(read))
		copy(buf, read[len(read)-kept:])
	}
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
	return fmt.Errorf("discovery: apps %q and %q are both built from the crate at %s: %q names the binary %q, and %q names none, so it runs %s: give both apps the same \"entrypoint\"", root.BinApp, root.DefaultBinApp, root.Dir, root.BinApp, bin.Name, root.DefaultBinApp, ran)
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
