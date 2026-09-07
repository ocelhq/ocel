package appbundler

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const goModuleFile = "go.mod"

type Compilation struct {
	App        string
	Runtime    Runtime
	Source     string
	Entrypoint string
	FuncDir    string
	AppDir     string
	Log        io.Writer
}

func (c Compilation) pkg() string {
	if c.Entrypoint == "" {
		return c.Source
	}
	return filepath.Join(c.Source, c.Entrypoint)
}

func Compile(ctx context.Context, c Compilation) error {
	if err := c.validate(); err != nil {
		return err
	}
	switch c.Runtime.Name {
	case providerkit.RuntimeGo:
		return c.compileGo(ctx)
	case providerkit.RuntimePython:
		return c.vendorPython(ctx)
	}
	return fmt.Errorf("app %q runs on runtime %q, which is not built from its own source tree", c.App, c.Runtime.Name)
}

func (c Compilation) compileGo(ctx context.Context) error {
	module, err := os.Stat(filepath.Join(c.Source, goModuleFile))
	if err != nil || !module.Mode().IsRegular() {
		return fmt.Errorf("app %q runs on the go runtime and %s holds no %s: an app is compiled from the module rooted in its own directory", c.App, c.Source, goModuleFile)
	}
	arch, runs := providerkit.GoArch(c.Runtime.Arch)
	if !runs {
		return fmt.Errorf("app %q asks to be compiled for %q, which names no architecture go builds for", c.App, c.Runtime.Arch)
	}
	if _, err := exec.LookPath("go"); err != nil {
		return fmt.Errorf("app %q runs on the go runtime and no go toolchain is on PATH: %w", c.App, err)
	}
	if err := os.MkdirAll(c.FuncDir, 0o755); err != nil {
		return err
	}
	binary := filepath.Join(c.FuncDir, c.App)
	cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-ldflags=-s -w", "-o", binary, ".")
	cmd.Dir = c.pkg()
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK=off", "GOOS=linux", "GOARCH="+arch)
	var said bytes.Buffer
	cmd.Stdout = &said
	cmd.Stderr = &said
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("compile app %q in %s for linux/%s (%w):\n%s", c.App, c.pkg(), arch, err, said.String())
	}
	c.report("compiling", said.String())
	return describeArtifact(c.App, c.Runtime, c.App, []string{"./" + c.App}, c.FuncDir, c.AppDir)
}

func (c Compilation) report(what, said string) {
	if c.Log != nil && strings.TrimSpace(said) != "" {
		fmt.Fprintf(c.Log, "ocel: %s %s reported:\n%s\n", what, c.App, strings.TrimRight(said, "\n"))
	}
}

func (c Compilation) validate() error {
	stated := []struct {
		name  string
		value string
	}{
		{"app", c.App},
		{"appDir", c.AppDir},
		{"source", c.Source},
		{"runtime", c.Runtime.Name},
		{"funcDir", c.FuncDir},
	}
	var missing []string
	for _, field := range stated {
		if field.value == "" {
			missing = append(missing, field.name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("cannot compile: %s not stated", strings.Join(missing, ", "))
	}
	if c.Runtime.Name == providerkit.RuntimePython && c.Entrypoint != "" {
		return fmt.Errorf("app %q runs on the python runtime and names entrypoint %q: a python app is served by the %s in its own directory, and both the artifact and the image are built from that, so an entrypoint here would name a file nothing boots", c.App, c.Entrypoint, pythonEntryFile)
	}
	pkg := c.pkg()
	info, err := os.Stat(pkg)
	if err != nil {
		return fmt.Errorf("package %s for app %q: %w", pkg, c.App, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("package %s for app %q is not a directory holding a main package", pkg, c.App)
	}
	return nil
}
