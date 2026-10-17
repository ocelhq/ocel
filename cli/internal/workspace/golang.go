package workspace

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/railwayapp/railpack/core/plan"
	"github.com/railwayapp/railpack/core/providers/golang"

	"github.com/ocelhq/ocel/cli/internal/gomodule"
	"github.com/ocelhq/ocel/cli/internal/language"
)

const (
	goWorkFile    = "go.work"
	goModDownload = "go mod download"
)

func findGoModule(dir string) (gomodule.Module, bool, error) {
	written, _ := language.Of(dir)
	if written != language.Go && (written != "" || !language.HasGoSource(dir)) {
		return gomodule.Module{}, false, nil
	}
	return gomodule.FindEnclosing(dir, atBoundary)
}

func findGoContext(module gomodule.Module) (string, error) {
	repository := module.Dir
	for !atBoundary(repository) {
		repository = filepath.Dir(repository)
	}
	root := module.Dir
	for _, replaced := range module.ReplacementDirs {
		for !under(root, replaced) {
			root = filepath.Dir(root)
		}
		if !under(repository, root) || filepath.Dir(root) == root {
			return "", fmt.Errorf(
				"the %s at %s replaces a module with %s, and the only directory holding both is %s, outside %s: an image is built from one directory holding the module and every directory it replaces a module with, and never from above the repository the app is checked out in or from the filesystem root — move %s inside %s, or require a published version of the module it holds",
				gomodule.File, module.Dir, replaced, root, repository, replaced, repository,
			)
		}
	}
	return root, nil
}

func (l Location) planGoCommands() Commands {
	if l.Path == "." {
		return Commands{}
	}
	binary := strings.Repeat("../", strings.Count(l.Path, "/")+1) + golang.GO_BINARY_NAME
	commands := Commands{
		Build: fmt.Sprintf(`cd %s && GOWORK=off CGO_ENABLED=0 go build -ldflags="-w -s" -o %s .`, l.Path, binary),
	}
	if l.ModuleDir != "." && isGoModuleOrWorkspaceRoot(l.Root) {
		commands.Install = plan.ShellCommandString(fmt.Sprintf("cd %s && GOWORK=off %s", l.ModuleDir, goModDownload))
	}
	return commands
}

func isGoModuleOrWorkspaceRoot(root string) bool {
	return isRegularFile(filepath.Join(root, gomodule.File)) || isRegularFile(filepath.Join(root, goWorkFile))
}

func locateGoApp(dir, moduleDir, goVersion, root string) (Location, error) {
	path, err := filepath.Rel(root, dir)
	if err != nil {
		return Location{}, err
	}
	module, err := filepath.Rel(root, moduleDir)
	if err != nil {
		return Location{}, err
	}
	return Location{
		Root:      root,
		Path:      filepath.ToSlash(path),
		Language:  language.Go,
		ModuleDir: filepath.ToSlash(module),
		GoVersion: goVersion,
	}, nil
}
