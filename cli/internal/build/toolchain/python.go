package toolchain

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/childprocess"

	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/pythonserver"
	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/containerimage"
)

const (
	pythonBytecodeDir   = "__pycache__"
	pythonVirtualenvDir = "venv"
	distInfoSuffix      = ".dist-info"
	asgiServer          = "uvicorn"
)

var asgiFrameworks = []string{"starlette", "quart", "litestar", "blacksheep"}

func (c Compilation) vendorPython(ctx context.Context) error {
	entrypoint, err := pythonserver.ResolveEntrypoint(c.Source, c.Entrypoint)
	if err != nil {
		return fmt.Errorf("app %q is built with python and %w", c.App, err)
	}
	platform, runs := arch.PythonPlatformTag(c.Framework.Arch)
	if !runs {
		return fmt.Errorf("app %q asks to be vendored for %q, which names no architecture wheels are built for", c.App, c.Framework.Arch)
	}
	if err := os.RemoveAll(c.FunctionDir); err != nil {
		return fmt.Errorf("reset %s: %w", c.FunctionDir, err)
	}
	if err := os.MkdirAll(c.FunctionDir, 0o755); err != nil {
		return err
	}
	if err := copySourceTree(c.Source, c.FunctionDir); err != nil {
		return fmt.Errorf("copy app %q into its artifact: %w", c.App, err)
	}
	if err := c.copyPythonPackages(); err != nil {
		return err
	}
	if err := c.installDependencies(ctx, platform); err != nil {
		return err
	}
	if err := c.writePythonServer(); err != nil {
		return err
	}
	if err := c.refuseUnservedASGI(ctx, entrypoint); err != nil {
		return err
	}
	worker, err := c.writePythonWorkerEntry()
	if err != nil {
		return err
	}
	return describeArtifact(c.App, c.Framework, pythonserver.File, entrypoint.Command(), worker, c.FunctionDir, c.AppDir)
}

func (c Compilation) refuseUnservedASGI(ctx context.Context, entrypoint pythonserver.Entrypoint) error {
	entries, err := os.ReadDir(c.FunctionDir)
	if err != nil {
		return err
	}
	installed := map[string]bool{}
	for _, entry := range entries {
		if name, ok := strings.CutSuffix(entry.Name(), distInfoSuffix); ok && entry.IsDir() {
			distribution, _, _ := strings.Cut(name, "-")
			installed[strings.ToLower(distribution)] = true
		}
	}
	if installed[asgiServer] {
		return nil
	}
	framework := ""
	for _, candidate := range asgiFrameworks {
		if installed[candidate] {
			framework = candidate
			break
		}
	}
	if framework == "" {
		return nil
	}
	definesApp, err := entrypoint.DefinesApp(ctx, discovery.PythonInterpreter(c.Source), c.FunctionDir)
	if err != nil {
		return fmt.Errorf("app %q: %w", c.App, err)
	}
	if !definesApp {
		return nil
	}
	return fmt.Errorf("app %q serves the %s app in %s with %s, which is not installed: add %s to its dependencies, or make the entrypoint start its own server", c.App, framework, entrypoint, asgiServer, asgiServer)
}

func (c Compilation) writePythonServer() error {
	if err := os.WriteFile(filepath.Join(c.FunctionDir, pythonserver.File), []byte(pythonserver.Source), 0o644); err != nil {
		return fmt.Errorf("write app %q's server program: %w", c.App, err)
	}
	return nil
}

func (c Compilation) writePythonWorkerEntry() ([]string, error) {
	if len(c.PythonPackages) == 0 {
		return nil, nil
	}
	entry := discovery.RenderPythonWorkerEntry(c.PythonPackages)
	if err := os.WriteFile(filepath.Join(c.FunctionDir, containerimage.PythonWorkerEntry.ArtifactFile), []byte(entry), 0o644); err != nil {
		return nil, fmt.Errorf("write app %q's worker entry: %w", c.App, err)
	}
	return containerimage.PythonWorkerEntry.ArtifactCommand(), nil
}

func (c Compilation) copyPythonPackages() error {
	for _, pkg := range c.PythonPackages {
		if rel, err := filepath.Rel(c.Source, pkg.Dir); err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, "../") {
			continue
		}
		if err := copySourceTree(pkg.Dir, filepath.Join(c.FunctionDir, pkg.Name)); err != nil {
			return fmt.Errorf("copy the %s app %q imports into its artifact: %w", pkg.Name, c.App, err)
		}
	}
	return nil
}

func (c Compilation) installDependencies(ctx context.Context, platform string) error {
	scratch, err := os.MkdirTemp("", "ocel-python-dependencies-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	deps, err := c.exportDependencies(ctx, scratch)
	if err != nil || deps.requirements == "" {
		return err
	}
	cmd := exec.Command(discovery.PythonInterpreter(c.Source), pipArgs(c.FunctionDir, deps.requirements, platform)...)
	cmd.Dir = c.Source
	var said bytes.Buffer
	cmd.Stdout = &said
	cmd.Stderr = &said
	if err := childprocess.Run(ctx, cmd); err != nil {
		return fmt.Errorf("vendor what app %q's %s declares for %s (%w):\n%s", c.App, deps.declaredIn, platform, err, said.String())
	}
	c.report("vendoring", said.String())
	return nil
}

func pipArgs(target, requirements, platform string) []string {
	return []string{
		"-I", "-m", "pip", "install",
		"--disable-pip-version-check",
		"--no-input",
		"--target", target,
		"-r", requirements,
		"--only-binary=:all:",
		"--platform", platform,
		"--python-version", arch.PythonVersion,
		"--implementation", "cp",
		"--no-compile",
	}
}

func copySourceTree(source, dest string) error {
	return copyTree(source, dest, leftBehindByTheBuildHost)
}

func copyTree(source, dest string, skip func(name string) bool) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel != "." && skip(entry.Name()) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return os.MkdirAll(filepath.Join(dest, rel), 0o755)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("%s is %s, and an artifact holds only regular files", path, entry.Type())
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return copyFile(path, filepath.Join(dest, rel), info.Mode().Perm())
	})
}

func leftBehindByTheBuildHost(name string) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	switch name {
	case pythonBytecodeDir, pythonVirtualenvDir, nodeModulesDir:
		return true
	}
	return false
}

func copyFile(from, to string, mode fs.FileMode) error {
	read, err := os.Open(from)
	if err != nil {
		return err
	}
	defer read.Close()
	written, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(written, read); err != nil {
		written.Close()
		return err
	}
	return written.Close()
}
