package build

import (
	"context"
	"io"
	"path/filepath"

	"github.com/ocelhq/ocel/cli/internal/build/toolchain"
	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/buildoutput"
)

func compiledFromSource(framework string) bool {
	return framework == buildoutput.FrameworkGo || framework == buildoutput.FrameworkPython || framework == buildoutput.FrameworkRust
}

func compile(ctx context.Context, cfg *project.Project, a project.App, outputDir string, env environment, log io.Writer) error {
	appDir := buildoutput.AppRoot(outputDir, a.Name)
	source := filepath.Join(cfg.Dir, a.Path)
	packages, err := listAppPythonPackages(cfg, a.Framework, source)
	if err != nil {
		return err
	}
	workerPackage, err := goWorkerPackage(cfg, a)
	if err != nil {
		return err
	}
	var goModuleDir string
	if a.GoModule != nil {
		goModuleDir = a.GoModule.Dir
	}
	return toolchain.Compile(ctx, toolchain.Compilation{
		App:            a.Name,
		Framework:      buildoutput.Framework{Name: a.Framework, Arch: a.Architecture()},
		Source:         source,
		GoModuleDir:    goModuleDir,
		GoPackage:      a.GoPackage,
		Entrypoint:     a.Entrypoint,
		WorkerPackage:  workerPackage,
		FunctionDir:    filepath.Join(appDir, buildoutput.FunctionsDir, buildoutput.RootFunctionDir),
		AppDir:         appDir,
		PythonPackages: packages,
		Env:            env.set,
		Unset:          env.unset,
		Log:            log,
	})
}

func listAppPythonPackages(cfg *project.Project, framework, source string) ([]discovery.PythonPackage, error) {
	if framework != buildoutput.FrameworkPython {
		return nil, nil
	}
	roots, err := discovery.RootsOf(cfg)
	if err != nil {
		return nil, err
	}
	return discovery.ListPythonPackages(cfg.Dir, roots, source)
}

func goWorkerPackage(cfg *project.Project, a project.App) (string, error) {
	if a.Framework != buildoutput.FrameworkGo || a.GoModule == nil {
		return "", nil
	}
	roots, err := discovery.RootsOf(cfg)
	if err != nil {
		return "", err
	}
	return discovery.GoWorkerPackage(cfg.Dir, roots, a.GoModule.Dir)
}
