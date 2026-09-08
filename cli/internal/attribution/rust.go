package attribution

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/discovery"
)

type rustReach struct{}

func (rustReach) Entries(ctx context.Context, root string, app App) (map[string]Reachability, error) {
	dir := filepath.Join(root, filepath.FromSlash(app.Path))
	metadata, err := discovery.CargoMetadata(ctx, dir)
	if err != nil {
		return nil, fmt.Errorf("attribution: app %q: %w", app.Name, err)
	}
	crate, ok := metadata.PackageAt(dir)
	if !ok {
		return nil, fmt.Errorf("attribution: app %q: the Cargo.toml at %s names no package", app.Name, dir)
	}

	reached := reachedDirs(root, metadata, crate, app.Roots)
	entries := map[string]Reachability{}
	for _, bin := range crate.Bins() {
		entry, inside := relativeToRoot(root, bin.SrcPath)
		if !inside {
			continue
		}
		entries[entry] = func(file string) bool { return under(reached, file) }
	}
	return entries, nil
}

func reachedDirs(root string, metadata discovery.Cargo, crate discovery.CargoPackage, roots []discovery.Root) []string {
	dirs := []string{crate.Dir()}
	for _, member := range workspaceDependencies(metadata, crate) {
		dirs = append(dirs, member.Dir())
	}
	for _, r := range roots {
		if r.Language == discovery.Rust {
			dirs = append(dirs, r.Dir)
		}
	}

	var relative []string
	for _, dir := range dirs {
		if rel, inside := relativeToRoot(root, dir); inside && !slices.Contains(relative, rel) {
			relative = append(relative, rel)
		}
	}
	return relative
}

func workspaceDependencies(metadata discovery.Cargo, crate discovery.CargoPackage) []discovery.CargoPackage {
	if metadata.Resolve == nil {
		return nil
	}
	nodes := make(map[string]discovery.CargoNode, len(metadata.Resolve.Nodes))
	for _, node := range metadata.Resolve.Nodes {
		nodes[node.ID] = node
	}

	var members []discovery.CargoPackage
	seen := map[string]bool{crate.ID: true}
	queue := []string{crate.ID}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for _, dep := range nodes[id].Deps {
			if seen[dep.Pkg] || !slices.Contains(metadata.WorkspaceMembers, dep.Pkg) {
				continue
			}
			seen[dep.Pkg] = true
			queue = append(queue, dep.Pkg)
			if member, ok := metadata.PackageByID(dep.Pkg); ok {
				members = append(members, member)
			}
		}
	}
	return members
}

func under(dirs []string, file string) bool {
	for _, dir := range dirs {
		if dir == "." || file == dir || strings.HasPrefix(file, dir+"/") {
			return true
		}
	}
	return false
}
