package attribution

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/cargo"
)

func readRustImports(ctx context.Context, root string, app App) (map[string]Reachability, error) {
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	dir := filepath.Join(root, filepath.FromSlash(app.Path))
	workspace, err := cargo.Metadata(ctx, dir, "--offline", "--no-deps")
	if err != nil {
		return nil, fmt.Errorf("attribution: app %q: %w", app.Name, err)
	}
	crate, ok := workspace.PackageAt(dir)
	if !ok {
		return nil, fmt.Errorf("attribution: app %q: the Cargo.toml at %s names no package", app.Name, dir)
	}

	bins := crate.Bins()
	if len(bins) > 1 {
		return nil, fmt.Errorf("attribution: app %q builds %d binaries, and ocel attributes one binary per app", app.Name, len(bins))
	}

	reached := reachedDirs(root, workspace, crate)
	entries := map[string]Reachability{}
	for _, bin := range bins {
		entry, inside := relativeToRoot(root, bin.SrcPath)
		if !inside {
			continue
		}
		entries[entry] = func(file string) bool { return under(reached, file) }
	}
	return entries, nil
}

func reachedDirs(root string, workspace cargo.Workspace, crate cargo.Package) []string {
	dirs := []string{crate.Dir()}
	for _, member := range workspaceDependencies(workspace, crate) {
		dirs = append(dirs, member.Dir())
	}

	var relative []string
	for _, dir := range dirs {
		if rel, inside := relativeToRoot(root, dir); inside && !slices.Contains(relative, rel) {
			relative = append(relative, rel)
		}
	}
	return relative
}

func workspaceDependencies(workspace cargo.Workspace, crate cargo.Package) []cargo.Package {
	members := map[string]cargo.Package{}
	for _, member := range workspace.Members() {
		members[member.Dir()] = member
	}

	var reached []cargo.Package
	seen := map[string]bool{crate.Dir(): true}
	queue := []cargo.Package{crate}
	for len(queue) > 0 {
		pkg := queue[0]
		queue = queue[1:]
		for _, dep := range pkg.Dependencies {
			member, ok := members[dep.Path]
			if !ok || seen[dep.Path] {
				continue
			}
			seen[dep.Path] = true
			queue = append(queue, member)
			reached = append(reached, member)
		}
	}
	return reached
}

func under(dirs []string, file string) bool {
	for _, dir := range dirs {
		if dir == "." || file == dir || strings.HasPrefix(file, dir+"/") {
			return true
		}
	}
	return false
}
