package discovery

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/workdir"

	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/cli/node"
	"github.com/ocelhq/ocel/pkg/processenv"
)

const nodeWorkerEntryFile = "worker.mjs"

const importsMarker = "ocel: imports"

//go:embed worker_main.go.tmpl
var goWorkerMain string

//go:embed worker.py
var pythonWorkerServer string

type workerCommand func(ctx context.Context, configDir string, roots []Root, served Root, run *workdir.Run) (*exec.Cmd, error)

var workerCommands = map[language.Language]workerCommand{
	language.JS:     nodeWorkerCommand,
	language.Go:     goWorkerCommand,
	language.Python: pythonWorkerCommand,
	language.Rust:   rustWorkerCommand,
}

func WorkerCommand(ctx context.Context, configDir string, roots []Root, served Root, run *workdir.Run) (*exec.Cmd, error) {
	command, ok := workerCommands[served.Language]
	if !ok {
		return nil, fmt.Errorf("discovery: %s is a %s folder, and ocel runs no worker from one", served.Dir, served.Language)
	}
	return command(ctx, configDir, roots, served, run)
}

func nodeWorkerCommand(ctx context.Context, configDir string, roots []Root, _ Root, run *workdir.Run) (*exec.Cmd, error) {
	bundle, err := bundleNodeWorker(configDir, roots, run.Path(configDir, nodeWorkerEntryFile), node.Bundle.TracedWorkerServerPath)
	if err != nil {
		return nil, err
	}
	entry, err := run.Write(configDir, nodeWorkerEntryFile, bundle)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("node", "--enable-source-maps", entry)
	cmd.Env = os.Environ()
	return cmd, nil
}

func BundleNodeWorker(configDir string, roots []Root, outfile string) error {
	bundle, err := bundleNodeWorker(configDir, roots, outfile, node.Bundle.WorkerServerPath)
	if err != nil {
		return err
	}
	if err := os.WriteFile(outfile, bundle, 0o644); err != nil {
		return fmt.Errorf("write the worker entry: %w", err)
	}
	return nil
}

func bundleNodeWorker(configDir string, roots []Root, outfile string, server func(node.Bundle) string) ([]byte, error) {
	source, resolveDir, err := renderNodeWorkerEntryOfRoots(configDir, roots, server)
	if err != nil {
		return nil, err
	}
	if source == "" {
		if source, err = renderNodeWorkerEntry(configDir, nil, server); err != nil {
			return nil, err
		}
	}
	if resolveDir == "" {
		resolveDir = configDir
	}
	bundle, err := bundleTo(resolveDir, outfile, "ocel-worker-entry.ts", source)
	if err != nil {
		return nil, fmt.Errorf("bundle the worker entry:\n%w", err)
	}
	return bundle, nil
}

func RenderNodeWorkerEntry(projectDir string, roots []Root) (source, resolveDir string, err error) {
	return renderNodeWorkerEntryOfRoots(projectDir, roots, node.Bundle.WorkerServerPath)
}

func renderNodeWorkerEntryOfRoots(projectDir string, roots []Root, server func(node.Bundle) string) (source, resolveDir string, err error) {
	var files []string
	for _, root := range roots {
		if root.Language != language.JS {
			continue
		}
		if resolveDir == "" {
			resolveDir = root.Dir
		}
		found, err := walkSourceFiles(root.Dir)
		if err != nil {
			return "", "", fmt.Errorf("discover resources: %w", err)
		}
		files = append(files, found...)
	}
	if len(files) == 0 {
		return "", "", nil
	}
	source, err = renderNodeWorkerEntry(projectDir, files, server)
	if err != nil {
		return "", "", err
	}
	return source, resolveDir, nil
}

func renderNodeWorkerEntry(projectDir string, files []string, server func(node.Bundle) string) (string, error) {
	installed, err := node.Ensure()
	if err != nil {
		return "", err
	}
	var entry strings.Builder
	fmt.Fprintf(&entry, "import { serve } from %q;\n", server(installed))
	entry.WriteString("import { deliver } from \"ocel/worker\";\n")
	fmt.Fprintf(&entry, "await serve(deliver, %q, [\n", processenv.WorkerEnvVar)
	if err := writeModuleImports(&entry, projectDir, files); err != nil {
		return "", err
	}
	entry.WriteString("]);\n")
	return entry.String(), nil
}

func goWorkerCommand(ctx context.Context, configDir string, roots []Root, served Root, _ *workdir.Run) (*exec.Cmd, error) {
	moduleRoot, pkg, err := WriteGoWorkerEntry(configDir, roots, served)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("go", "run", pkg)
	cmd.Dir = moduleRoot
	cmd.Env = os.Environ()
	return cmd, nil
}

func WriteGoWorkerEntry(configDir string, roots []Root, served Root) (string, string, error) {
	moduleRoot, _, err := goPackage(configDir, served)
	if err != nil {
		return "", "", err
	}
	pkg, err := writeGoWorkerEntry(configDir, roots, moduleRoot)
	if err != nil {
		return "", "", err
	}
	return moduleRoot, pkg, nil
}

func rustWorkerCommand(ctx context.Context, _ string, _ []Root, served Root, _ *workdir.Run) (*exec.Cmd, error) {
	cmd, _, err := cargoRunCommand(ctx, served)
	if err != nil {
		return nil, err
	}
	cmd.Env = os.Environ()
	return cmd, nil
}

func GoWorkerPackage(configDir string, roots []Root, moduleDir string) (string, error) {
	moduleRoot, err := filepath.Abs(moduleDir)
	if err != nil {
		return "", err
	}
	return writeGoWorkerEntry(configDir, roots, moduleRoot)
}

func writeGoWorkerEntry(configDir string, roots []Root, moduleRoot string) (string, error) {
	var imports strings.Builder
	for _, root := range roots {
		if root.Language != language.Go {
			continue
		}
		rootModule, pkg, err := goPackage(configDir, root)
		if err != nil {
			return "", err
		}
		if rootModule == moduleRoot {
			fmt.Fprintf(&imports, "_ %q\n\t", pkg)
		}
	}
	if imports.Len() == 0 {
		return "", nil
	}
	return writeGoEntry(moduleRoot, "worker", strings.Replace(goWorkerMain, "// "+importsMarker+"\n", strings.TrimSuffix(imports.String(), "\t"), 1))
}

func pythonWorkerCommand(ctx context.Context, configDir string, roots []Root, served Root, _ *workdir.Run) (*exec.Cmd, error) {
	runRoot, err := pythonRunRoot(configDir, served.Dir)
	if err != nil {
		return nil, err
	}
	packages, err := ListPythonPackages(configDir, roots, served.Dir)
	if err != nil {
		return nil, err
	}
	entry, err := writePythonEntry(runRoot, "worker", RenderPythonWorkerEntry(packages))
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(PythonInterpreter(runRoot), entry)
	cmd.Dir = runRoot
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	if importDirs := listImportDirsOutside(runRoot, packages); len(importDirs) > 0 {
		if inherited := os.Getenv("PYTHONPATH"); inherited != "" {
			importDirs = append(importDirs, inherited)
		}
		cmd.Env = append(cmd.Env, "PYTHONPATH="+strings.Join(importDirs, string(os.PathListSeparator)))
	}
	return cmd, nil
}

func listImportDirsOutside(runRoot string, packages []PythonPackage) []string {
	var dirs []string
	for _, pkg := range packages {
		if dir := filepath.Dir(pkg.Dir); dir != runRoot && !slices.Contains(dirs, dir) {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

func RenderPythonWorkerEntry(packages []PythonPackage) string {
	var imports strings.Builder
	for _, pkg := range packages {
		fmt.Fprintf(&imports, "import_module(%s)\n", strconv.Quote(pkg.Name))
	}
	return strings.Replace(pythonWorkerServer, "# "+importsMarker+"\n", imports.String(), 1)
}

func WorkerRoot(configDir string, roots []Root, worker string, sources []string) (Root, error) {
	var served Root
	var servedAt string
	for _, source := range sources {
		colon := strings.LastIndex(source, ":")
		if colon <= 0 {
			continue
		}
		file := source[:colon]
		if !filepath.IsAbs(file) {
			file = filepath.Join(configDir, file)
		}
		at := displayedSite(configDir, file, source[colon+1:])
		root, found := rootContaining(roots, file)
		if !found {
			return Root{}, fmt.Errorf("worker %q serves %s, which is in no discovery folder", worker, at)
		}
		if servedAt == "" {
			served, servedAt = root, at
			continue
		}
		if root.Language != served.Language {
			return Root{}, fmt.Errorf("worker %q serves %s in %s and %s in %s, and a worker runs in one language: give one of them a worker of its own", worker, servedAt, served.Language, at, root.Language)
		}
	}
	if servedAt != "" {
		return served, nil
	}
	if len(roots) == 1 {
		return roots[0], nil
	}
	return Root{}, fmt.Errorf("worker %q serves nothing declared in a discovery folder", worker)
}

func WorkerRoots(configDir string, roots []Root, served Root) ([]Root, error) {
	switch served.Language {
	case language.JS:
		var js []Root
		for _, root := range roots {
			if root.Language == language.JS {
				js = append(js, root)
			}
		}
		return js, nil
	case language.Go:
		moduleRoot, _, err := goPackage(configDir, served)
		if err != nil {
			return nil, err
		}
		var module []Root
		for _, root := range roots {
			if root.Language != language.Go {
				continue
			}
			rootModule, _, err := goPackage(configDir, root)
			if err != nil {
				return nil, err
			}
			if rootModule == moduleRoot {
				module = append(module, root)
			}
		}
		return module, nil
	case language.Python:
		packages, err := ListPythonPackages(configDir, roots, served.Dir)
		if err != nil {
			return nil, err
		}
		imported := make([]Root, 0, len(packages))
		for _, pkg := range packages {
			imported = append(imported, Root{Dir: pkg.Dir, Language: language.Python})
		}
		return imported, nil
	}
	return []Root{served}, nil
}

func displayedSite(configDir, file, line string) string {
	if isWithin(configDir, file) {
		rel, _ := filepath.Rel(configDir, file)
		file = filepath.ToSlash(rel)
	}
	return file + ":" + line
}

func rootContaining(roots []Root, file string) (Root, bool) {
	var deepest Root
	found := false
	for _, root := range roots {
		if !isWithin(root.Dir, file) {
			continue
		}
		if !found || len(root.Dir) > len(deepest.Dir) {
			deepest, found = root, true
		}
	}
	return deepest, found
}

func isWithin(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
