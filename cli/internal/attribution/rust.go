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

	patchedDirs, err := cargo.ReadPatchedDirs(workspace.Root)
	if err != nil {
		return nil, fmt.Errorf("attribution: app %q: %w", app.Name, err)
	}

	entries := map[string]Reachability{}
	if len(crate.Bins()) == 0 {
		return entries, nil
	}
	bin, err := crate.FindBin(app.Entrypoint)
	if err != nil {
		return nil, fmt.Errorf("attribution: app %q runs one binary of its crate: %w", app.Name, err)
	}
	reached := reachedDirs(root, workspace, crate, patchedDirs)
	if entry, inside := relativeToRoot(root, bin.SrcPath); inside {
		entries[entry] = func(file string) bool { return under(reached, file) }
	}
	return entries, nil
}

func reachedDirs(root string, workspace cargo.Workspace, crate cargo.Package, patchedDirs []string) []string {
	dirs := []string{crate.Dir()}
	for _, member := range workspaceDependencies(workspace, crate, patchedDirs) {
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

func workspaceDependencies(workspace cargo.Workspace, crate cargo.Package, patchedDirs []string) []cargo.Package {
	members := map[string]cargo.Package{}
	for _, member := range workspace.Members() {
		members[member.Dir()] = member
	}
	patched := map[string]string{}
	for _, dir := range patchedDirs {
		if member, ok := members[dir]; ok {
			patched[member.Name] = dir
		}
	}

	var reached []cargo.Package
	requested := map[string]map[string]bool{crate.Dir(): {"default": true}}
	queue := []cargo.Package{crate}
	for len(queue) > 0 {
		pkg := queue[0]
		queue = queue[1:]
		enabled, depFeatures := resolveFeatures(pkg, requested[pkg.Dir()])
		for _, dep := range pkg.Dependencies {
			dir := dep.Path
			if dir == "" {
				dir = patched[dep.Name]
			}
			member, ok := members[dir]
			if !ok || dep.Optional && !enabled[dep.Key()] {
				continue
			}
			features := append(slices.Clone(dep.Features), depFeatures[dep.Key()]...)
			if dep.UsesDefaultFeatures {
				features = append(features, "default")
			}
			first := requested[dir] == nil
			if first {
				requested[dir] = map[string]bool{}
				reached = append(reached, member)
			}
			grew := false
			for _, feature := range features {
				if !requested[dir][feature] {
					requested[dir][feature] = true
					grew = true
				}
			}
			if first || grew {
				queue = append(queue, member)
			}
		}
	}
	return reached
}

func resolveFeatures(pkg cargo.Package, requested map[string]bool) (map[string]bool, map[string][]string) {
	enabled := map[string]bool{}
	depFeatures := map[string][]string{}
	seen := map[string]bool{}
	var pending []string
	for feature := range requested {
		pending = append(pending, feature)
	}
	for len(pending) > 0 {
		value := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if seen[value] {
			continue
		}
		seen[value] = true
		if dep, ok := strings.CutPrefix(value, "dep:"); ok {
			enabled[dep] = true
			continue
		}
		if dep, feature, ok := strings.Cut(value, "/"); ok {
			if name, weak := strings.CutSuffix(dep, "?"); weak {
				dep = name
			} else {
				enabled[dep] = true
			}
			depFeatures[dep] = append(depFeatures[dep], feature)
			continue
		}
		pending = append(pending, pkg.Features[value]...)
	}
	return enabled, depFeatures
}

func under(dirs []string, file string) bool {
	for _, dir := range dirs {
		if dir == "." || file == dir || strings.HasPrefix(file, dir+"/") {
			return true
		}
	}
	return false
}
