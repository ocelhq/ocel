package appbundler

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const (
	pythonEntryFile        = "main.py"
	pythonRequirementsFile = "requirements.txt"
	pythonProgram          = "python3"
	pythonBytecodeDir      = "__pycache__"
)

func (c Compilation) vendorPython(ctx context.Context) error {
	entry, err := os.Stat(filepath.Join(c.Source, pythonEntryFile))
	if err != nil || !entry.Mode().IsRegular() {
		return fmt.Errorf("app %q runs on the python runtime and %s holds no %s: an app is served by the module rooted in its own directory", c.App, c.Source, pythonEntryFile)
	}
	platform, runs := providerkit.PythonPlatformTag(c.Runtime.Arch)
	if !runs {
		return fmt.Errorf("app %q asks to be vendored for %q, which names no architecture wheels are built for", c.App, c.Runtime.Arch)
	}
	if err := os.RemoveAll(c.FuncDir); err != nil {
		return fmt.Errorf("reset %s: %w", c.FuncDir, err)
	}
	if err := os.MkdirAll(c.FuncDir, 0o755); err != nil {
		return err
	}
	if err := copySourceTree(c.Source, c.FuncDir); err != nil {
		return fmt.Errorf("carry app %q into its artifact: %w", c.App, err)
	}
	if err := c.installRequirements(ctx, platform); err != nil {
		return err
	}
	return describeArtifact(c.App, c.Runtime, pythonEntryFile, []string{pythonProgram, pythonEntryFile}, c.FuncDir, c.AppDir)
}

func (c Compilation) installRequirements(ctx context.Context, platform string) error {
	requirements := filepath.Join(c.Source, pythonRequirementsFile)
	declared, err := os.Stat(requirements)
	if err != nil || !declared.Mode().IsRegular() {
		return nil
	}
	program, err := exec.LookPath(pythonProgram)
	if err != nil {
		return fmt.Errorf("app %q declares dependencies in %s and no %s is on PATH to vendor them with: %w", c.App, pythonRequirementsFile, pythonProgram, err)
	}
	cmd := exec.CommandContext(ctx, program, pipArgs(c.FuncDir, requirements, platform)...)
	cmd.Dir = c.Source
	var said bytes.Buffer
	cmd.Stdout = &said
	cmd.Stderr = &said
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("vendor app %q's %s for %s (%w):\n%s", c.App, pythonRequirementsFile, platform, err, said.String())
	}
	c.report("vendoring", said.String())
	return nil
}

func pipArgs(target, requirements, platform string) []string {
	return []string{
		"-m", "pip", "install",
		"--disable-pip-version-check",
		"--no-input",
		"--target", target,
		"-r", requirements,
		"--only-binary=:all:",
		"--platform", platform,
		"--python-version", providerkit.PythonVersion,
		"--implementation", "cp",
	}
}

func copySourceTree(source, dest string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == pythonBytecodeDir {
				return fs.SkipDir
			}
			return os.MkdirAll(filepath.Join(dest, rel), 0o755)
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return copyFile(path, filepath.Join(dest, rel), info.Mode().Perm())
	})
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
