package attribution

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"path"
	"path/filepath"
)

type goModule struct {
	Dir string
}

type goPackage struct {
	ImportPath string
	Name       string
	Dir        string
	Deps       []string
	Module     *goModule
}

type goReach struct{}

func (goReach) Entries(ctx context.Context, root string, app App) (map[string]Reachability, error) {
	dir := filepath.Join(root, filepath.FromSlash(app.Path))
	packages, err := goList(ctx, app.Name, dir)
	if err != nil {
		return nil, err
	}

	dirs := make(map[string]string, len(packages))
	for _, p := range packages {
		dirs[p.ImportPath] = p.Dir
	}

	entries := map[string]Reachability{}
	for _, p := range packages {
		if p.Name != "main" {
			continue
		}
		entry, inside := relativeToRoot(root, p.Dir)
		if !inside || p.Module == nil {
			continue
		}
		reached := map[string]bool{}
		for _, importPath := range append([]string{p.ImportPath}, p.Deps...) {
			if _, inside := relativeToRoot(p.Module.Dir, dirs[importPath]); !inside {
				continue
			}
			if rel, inside := relativeToRoot(root, dirs[importPath]); inside {
				reached[rel] = true
			}
		}
		entries[entry] = func(file string) bool { return reached[path.Dir(file)] }
	}
	return entries, nil
}

func goList(ctx context.Context, app, dir string) ([]goPackage, error) {
	cmd := exec.CommandContext(ctx, "go", "list", "-json", "-deps", "./...")
	cmd.Dir = dir
	decoder, said, err := runJSON(cmd)
	if err != nil {
		return nil, fmt.Errorf("attribution: app %q: go list: %s", app, said)
	}

	var packages []goPackage
	for {
		var p goPackage
		if err := decoder.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("attribution: app %q: read what go list said: %w", app, err)
		}
		packages = append(packages, p)
	}
	return packages, nil
}
