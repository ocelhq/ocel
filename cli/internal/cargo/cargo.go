package cargo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

type Target struct {
	Name    string   `json:"name"`
	Kind    []string `json:"kind"`
	SrcPath string   `json:"src_path"`
}

type Package struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	ManifestPath string   `json:"manifest_path"`
	Targets      []Target `json:"targets"`
}

type Dependency struct {
	Pkg string `json:"pkg"`
}

type Node struct {
	ID   string       `json:"id"`
	Deps []Dependency `json:"deps"`
}

type Resolve struct {
	Nodes []Node `json:"nodes"`
}

type Workspace struct {
	Packages []Package `json:"packages"`
	Members  []string  `json:"workspace_members"`
	Root     string    `json:"workspace_root"`
	Resolve  *Resolve  `json:"resolve"`
}

func (p Package) Dir() string { return filepath.Dir(p.ManifestPath) }

func (p Package) Bins() []Target {
	var bins []Target
	for _, target := range p.Targets {
		if slices.Contains(target.Kind, "bin") {
			bins = append(bins, target)
		}
	}
	slices.SortFunc(bins, func(a, b Target) int { return strings.Compare(a.Name, b.Name) })
	return bins
}

func (w Workspace) PackageAt(dir string) (Package, bool) {
	for _, p := range w.Packages {
		if p.Dir() == dir {
			return p, true
		}
	}
	return Package{}, false
}

func (w Workspace) PackageByID(id string) (Package, bool) {
	for _, p := range w.Packages {
		if p.ID == id {
			return p, true
		}
	}
	return Package{}, false
}

func (w Workspace) WorkspaceMembers() []Package {
	var members []Package
	for _, id := range w.Members {
		if member, ok := w.PackageByID(id); ok {
			members = append(members, member)
		}
	}
	return members
}

func Metadata(ctx context.Context, dir string, opts ...string) (Workspace, error) {
	args := append([]string{"metadata", "--format-version", "1"}, opts...)
	cmd := exec.CommandContext(ctx, "cargo", args...)
	cmd.Dir = dir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if said := strings.TrimSpace(stderr.String()); said != "" {
			return Workspace{}, fmt.Errorf("cargo metadata in %s: %s", dir, said)
		}
		return Workspace{}, fmt.Errorf("cargo metadata in %s: %w", dir, err)
	}

	var workspace Workspace
	if err := json.Unmarshal(stdout.Bytes(), &workspace); err != nil {
		return Workspace{}, fmt.Errorf("cargo metadata in %s: read what cargo said: %w", dir, err)
	}
	return workspace, nil
}
