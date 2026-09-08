package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

type CargoTarget struct {
	Name    string   `json:"name"`
	Kind    []string `json:"kind"`
	SrcPath string   `json:"src_path"`
}

type CargoPackage struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	ManifestPath string        `json:"manifest_path"`
	Targets      []CargoTarget `json:"targets"`
}

type CargoDependency struct {
	Pkg string `json:"pkg"`
}

type CargoNode struct {
	ID   string            `json:"id"`
	Deps []CargoDependency `json:"deps"`
}

type CargoResolve struct {
	Nodes []CargoNode `json:"nodes"`
}

type Cargo struct {
	Packages         []CargoPackage `json:"packages"`
	WorkspaceMembers []string       `json:"workspace_members"`
	WorkspaceRoot    string         `json:"workspace_root"`
	Resolve          *CargoResolve  `json:"resolve"`
}

func (p CargoPackage) Dir() string { return filepath.Dir(p.ManifestPath) }

func (p CargoPackage) Bins() []CargoTarget {
	var bins []CargoTarget
	for _, target := range p.Targets {
		if slices.Contains(target.Kind, "bin") {
			bins = append(bins, target)
		}
	}
	slices.SortFunc(bins, func(a, b CargoTarget) int { return strings.Compare(a.Name, b.Name) })
	return bins
}

func (c Cargo) PackageAt(dir string) (CargoPackage, bool) {
	for _, p := range c.Packages {
		if p.Dir() == dir {
			return p, true
		}
	}
	return CargoPackage{}, false
}

func (c Cargo) PackageByID(id string) (CargoPackage, bool) {
	for _, p := range c.Packages {
		if p.ID == id {
			return p, true
		}
	}
	return CargoPackage{}, false
}

func CargoMetadata(ctx context.Context, dir string, extra ...string) (Cargo, error) {
	args := append([]string{"metadata", "--format-version", "1"}, extra...)
	cmd := exec.CommandContext(ctx, "cargo", args...)
	cmd.Dir = dir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if said := strings.TrimSpace(stderr.String()); said != "" {
			return Cargo{}, fmt.Errorf("cargo metadata in %s: %s", dir, said)
		}
		return Cargo{}, fmt.Errorf("cargo metadata in %s: %w", dir, err)
	}

	var metadata Cargo
	if err := json.Unmarshal(stdout.Bytes(), &metadata); err != nil {
		return Cargo{}, fmt.Errorf("cargo metadata in %s: read what cargo said: %w", dir, err)
	}
	return metadata, nil
}

type rustLauncher struct{}

func (rustLauncher) Command(ctx context.Context, configDir string, root Root, serverURL string) (*exec.Cmd, error) {
	crateDir, found, err := walkUp(configDir, root.Dir, holdsACrate)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("discovery: %s is a rust folder, but no Cargo.toml stands between it and %s", root.Dir, configDir)
	}

	metadata, err := CargoMetadata(ctx, crateDir, "--no-deps")
	if err != nil {
		return nil, fmt.Errorf("discovery: %w", err)
	}
	crate, ok := metadata.PackageAt(crateDir)
	if !ok {
		return nil, fmt.Errorf("discovery: the Cargo.toml at %s names no package to run %s from", crateDir, root.Dir)
	}
	bins := crate.Bins()
	if len(bins) == 0 {
		return nil, fmt.Errorf("discovery: %s declares resources but %s builds no binary to run them from", root.Dir, crate.Name)
	}

	cmd := exec.CommandContext(ctx, "cargo", "run", "--quiet", "--manifest-path", crate.ManifestPath, "--bin", bins[0].Name)
	cmd.Dir = metadata.WorkspaceRoot
	cmd.Env = append(os.Environ(), "OCEL_PHASE=discovery", "OCEL_DEV_SERVER="+serverURL, "OCEL_SOURCE_ROOT="+metadata.WorkspaceRoot)
	return cmd, nil
}

func holdsACrate(at string) bool {
	info, err := os.Stat(filepath.Join(at, "Cargo.toml"))
	return err == nil && info.Mode().IsRegular()
}
