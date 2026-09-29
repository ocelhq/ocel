package toolchain

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/ocelhq/ocel/pkg/arch"
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
	if err := os.MkdirAll(c.FuncDir, 0o755); err != nil {
		return err
	}
	binary := filepath.Join(c.FuncDir, c.App)
	cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-ldflags=-s -w", "-o", binary, ".")
	cmd.Dir = c.pkg()
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK=off", "GOOS=linux", "GOARCH="+goarch)
	var said bytes.Buffer
	cmd.Stdout = &said
	cmd.Stderr = &said
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("compile app %q in %s for linux/%s (%w):\n%s", c.App, c.pkg(), goarch, err, said.String())
	}
	c.report("compiling", said.String())
	return describeArtifact(c.App, c.Framework, c.App, []string{"./" + c.App}, c.FuncDir, c.AppDir)
}
