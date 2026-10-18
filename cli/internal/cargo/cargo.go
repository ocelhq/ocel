package cargo

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

	"github.com/ocelhq/ocel/cli/internal/childprocess"

	"github.com/pelletier/go-toml/v2"
)

type Target struct {
	Name    string   `json:"name"`
	Kind    []string `json:"kind"`
	SrcPath string   `json:"src_path"`
}

type Dependency struct {
	Name                string   `json:"name"`
	Rename              string   `json:"rename"`
	Path                string   `json:"path"`
	Optional            bool     `json:"optional"`
	UsesDefaultFeatures bool     `json:"uses_default_features"`
	Features            []string `json:"features"`
}

func (d Dependency) Key() string {
	if d.Rename != "" {
		return d.Rename
	}
	return d.Name
}

type Package struct {
	ID           string              `json:"id"`
	Name         string              `json:"name"`
	ManifestPath string              `json:"manifest_path"`
	Targets      []Target            `json:"targets"`
	Dependencies []Dependency        `json:"dependencies"`
	Features     map[string][]string `json:"features"`
}

type Workspace struct {
	Packages  []Package `json:"packages"`
	MemberIDs []string  `json:"workspace_members"`
	Root      string    `json:"workspace_root"`
	Target    string    `json:"target_directory"`
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
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
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

func (w Workspace) Members() []Package {
	var members []Package
	for _, id := range w.MemberIDs {
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
	if err := childprocess.Run(ctx, cmd); err != nil {
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

func ReadPatchedDirs(workspaceRoot string) ([]string, error) {
	path := filepath.Join(workspaceRoot, "Cargo.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read the patches of %s: %w", path, err)
	}
	var manifest struct {
		Patch map[string]map[string]any `toml:"patch"`
	}
	if err := toml.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("read the patches of %s: %w", path, err)
	}

	var dirs []string
	for _, source := range manifest.Patch {
		for _, entry := range source {
			table, _ := entry.(map[string]any)
			dir, ok := table["path"].(string)
			if !ok {
				continue
			}
			if !filepath.IsAbs(dir) {
				dir = filepath.Join(workspaceRoot, dir)
			}
			dirs = append(dirs, filepath.Clean(dir))
		}
	}
	return dirs, nil
}
