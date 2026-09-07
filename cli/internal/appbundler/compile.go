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
)

const BootstrapFile = "bootstrap"

var goArchitectures = map[string]string{"x86_64": "amd64", "arm64": "arm64"}

type Compilation struct {
	App     string
	Runtime Runtime
	Package string
	FuncDir string
	AppDir  string
	Log     io.Writer
}

func Compile(ctx context.Context, c Compilation) error {
	if err := c.validate(); err != nil {
		return err
	}
	arch, runs := goArchitectures[c.Runtime.Arch]
	if !runs {
		return fmt.Errorf("app %q asks to be compiled for %q, which names no architecture go builds for", c.App, c.Runtime.Arch)
	}
	if _, err := exec.LookPath("go"); err != nil {
		return fmt.Errorf("app %q runs on the go runtime and no go toolchain is on PATH: %w", c.App, err)
	}
	if err := os.MkdirAll(c.FuncDir, 0o755); err != nil {
		return err
	}
	binary := filepath.Join(c.FuncDir, BootstrapFile)
	cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-ldflags=-s -w", "-o", binary, ".")
	cmd.Dir = c.Package
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+arch)
	var said bytes.Buffer
	cmd.Stdout = &said
	cmd.Stderr = &said
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("compile app %q in %s for linux/%s (%w):\n%s", c.App, c.Package, arch, err, said.String())
	}
	if c.Log != nil && said.Len() > 0 {
		fmt.Fprintf(c.Log, "ocel: compiling %s reported:\n%s\n", c.App, strings.TrimRight(said.String(), "\n"))
	}
	return describeArtifact(c.App, c.Runtime, BootstrapFile, c.FuncDir, c.AppDir)
}

func (c Compilation) validate() error {
	stated := []struct {
		name  string
		value string
	}{
		{"app", c.App},
		{"appDir", c.AppDir},
		{"package", c.Package},
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
	info, err := os.Stat(c.Package)
	if err != nil {
		return fmt.Errorf("package %s for app %q: %w", c.Package, c.App, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("package %s for app %q is not a directory holding a main package", c.Package, c.App)
	}
	return nil
}
