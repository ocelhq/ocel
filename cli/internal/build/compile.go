package build

import (
	"context"
	"io"
	"path/filepath"

	"github.com/ocelhq/ocel/cli/internal/build/toolchain"
	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/appbuild"
)

func compiledFromSource(framework string) bool {
	return framework == appbuild.FrameworkGo || framework == appbuild.FrameworkPython || framework == appbuild.FrameworkRust
}

func compile(ctx context.Context, cfg *project.Project, a project.App, outputDir string, log io.Writer) error {
	appDir := appbuild.AppArtifactRoot(outputDir, a.Name)
	roots, err := discoveryRootsFor(cfg, a.Framework.Name)
	if err != nil {
		return err
	}
	return toolchain.Compile(ctx, toolchain.Compilation{
		App:            a.Name,
		Framework:      appbuild.Framework{Name: a.Framework.Name, Arch: a.Framework.Architecture()},
		Source:         filepath.Join(cfg.Dir, a.Path),
		Entrypoint:     a.Entrypoint,
		FuncDir:        filepath.Join(appDir, functionsDirName, entryFuncDirName),
		AppDir:         appDir,
		DiscoveryRoots: roots,
		Log:            log,
	})
}

func discoveryRootsFor(cfg *project.Project, framework string) ([]string, error) {
	if framework != appbuild.FrameworkPython {
		return nil, nil
	}
	roots, err := discovery.RootsOf(cfg)
	if err != nil {
		return nil, err
	}
	var dirs []string
	for _, root := range roots {
		if root.Language == language.Python {
			dirs = append(dirs, root.Dir)
		}
	}
	return dirs, nil
}
