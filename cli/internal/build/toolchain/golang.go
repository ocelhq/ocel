package toolchain

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/buildoutput"
)

const goModuleFile = "go.mod"

func (c Compilation) compileGo(ctx context.Context) error {
	module, err := os.Stat(filepath.Join(c.Source, goModuleFile))
	if err != nil || !module.Mode().IsRegular() {
		return fmt.Errorf("app %q is built with go and %s has no %s: an app is compiled from the module rooted in its own directory", c.App, c.Source, goModuleFile)
	}
	goarch, runs := arch.GoArch(c.Framework.Arch)
	if !runs {
		return fmt.Errorf("app %q asks to be compiled for %q, which names no architecture go builds for", c.App, c.Framework.Arch)
	}
	if _, err := exec.LookPath("go"); err != nil {
		return fmt.Errorf("app %q is built with go and no go toolchain is on PATH: %w", c.App, err)
	}
	if err := os.MkdirAll(c.FunctionDir, 0o755); err != nil {
		return err
	}
	if err := c.buildGo(ctx, goarch, c.pkg(), ".", c.App); err != nil {
		return err
	}
	if c.WorkerPackage != "" {
		if err := c.buildGo(ctx, goarch, c.Source, c.WorkerPackage, buildoutput.GoWorkerBinary); err != nil {
			return err
		}
	}
	return describeArtifact(c.App, c.Framework, c.App, []string{"./" + c.App}, c.FunctionDir, c.AppDir)
}

func (c Compilation) buildGo(ctx context.Context, goarch, dir, pkg, binary string) error {
	cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-ldflags=-s -w", "-o", filepath.Join(c.FunctionDir, binary), pkg)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK=off", "GOOS=linux", "GOARCH="+goarch)
	var said bytes.Buffer
	cmd.Stdout = &said
	cmd.Stderr = &said
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("compile %s of app %q in %s for linux/%s (%w):\n%s", pkg, c.App, dir, goarch, err, said.String())
	}
	c.report("compiling", said.String())
	return nil
}
