package workspace

import (
	"fmt"
	"path"
	"path/filepath"

	"github.com/railwayapp/railpack/core/providers/golang"

	"github.com/ocelhq/ocel/cli/internal/gomodule"
	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/cli/internal/shellword"
)

const (
	goWorkFile    = "go.work"
	goModDownload = "go mod download"
)

func findGoContext(module gomodule.Module) (string, error) {
	root, outside := findContext(module.Dir, module.ReplacementDirs)
	if outside != "" {
		repository := findRepository(module.Dir)
		return "", fmt.Errorf(
			"the %s at %s replaces a module with %s, and the only directory holding both is %s, outside %s: an image is built from one directory holding the module and every directory it replaces a module with, and never from above the repository the app is checked out in or from the filesystem root — move %s inside %s, or require a published version of the module it holds",
			gomodule.File, module.Dir, outside, root, repository, outside, repository,
		)
	}
	return root, nil
}

func (l Location) planGoCommands() Commands {
	if l.ModuleDir == "" || l.GoPackage == "" || l.Entrypoint == "" && l.Path == "." {
		return Commands{}
	}
	binary := path.Join(findRelativeRoot(l.ModuleDir), golang.GO_BINARY_NAME)
	commands := Commands{Build: inDir(l.ModuleDir, `GOWORK=off CGO_ENABLED=0 go build -ldflags="-w -s" -o `+shellword.Quote(binary)+" "+shellword.Quote(l.GoPackage))}
	if l.ModuleDir != "." && isGoModuleOrWorkspaceRoot(l.Root) {
		commands.Install = "sh -c " + shellword.Quote(inDir(l.ModuleDir, "GOWORK=off "+goModDownload))
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
