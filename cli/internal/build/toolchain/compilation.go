package toolchain

import (
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/pkg/buildoutput"
)

type Compilation struct {
	App            string
	Framework      buildoutput.Framework
	Source         string
	Entrypoint     string
	GoModuleDir    string
	GoPackage      string
	WorkerPackage  string
	FunctionDir    string
	AppDir         string
	PythonPackages []discovery.PythonPackage
	Env            map[string]string
	Unset          []string
	Log            io.Writer
}

func (c Compilation) environ(own ...string) []string {
	environ := slices.DeleteFunc(os.Environ(), func(entry string) bool {
		name, _, _ := strings.Cut(entry, "=")
		_, overridden := c.Env[name]
		return overridden || slices.Contains(c.Unset, name)
	})
	for _, name := range slices.Sorted(maps.Keys(c.Env)) {
		environ = append(environ, name+"="+c.Env[name])
	}
	return append(environ, own...)
}

func Compile(ctx context.Context, c Compilation) error {
	if err := c.validate(); err != nil {
		return err
	}
	switch c.Framework.Name {
	case buildoutput.FrameworkGo:
		return c.compileGo(ctx)
	case buildoutput.FrameworkPython:
		return c.vendorPython(ctx)
	case buildoutput.FrameworkRust:
		return c.compileRust(ctx)
	}
	return fmt.Errorf("app %q is built with %q, which is not built from its own source tree", c.App, c.Framework.Name)
}

func (c Compilation) report(what, said string) {
	if c.Log != nil && strings.TrimSpace(said) != "" {
		fmt.Fprintf(c.Log, "ocel: %s %s reported:\n%s\n", what, c.App, strings.TrimRight(said, "\n"))
	}
}

func (c Compilation) validate() error {
	if err := refuseUnstated("compile", []statedField{
		{"app", c.App},
		{"appDir", c.AppDir},
		{"source", c.Source},
		{"framework", c.Framework.Name},
		{"functionDir", c.FunctionDir},
	}); err != nil {
		return err
	}
	if c.Framework.Name == buildoutput.FrameworkRust && c.Entrypoint != "" {
		return fmt.Errorf("app %q is built with rust and names entrypoint %q: a rust app is compiled from the one binary the %s in its own directory builds, so an entrypoint here would name nothing that is built", c.App, c.Entrypoint, cargoManifestFile)
	}
	info, err := os.Stat(c.Source)
	if err != nil {
		return fmt.Errorf("source %s for app %q: %w", c.Source, c.App, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("source %s for app %q is not a directory", c.Source, c.App)
	}
	return nil
}

type statedField struct {
	name  string
	value string
}

func refuseUnstated(action string, fields []statedField) error {
	var missing []string
	for _, field := range fields {
		if field.value == "" {
			missing = append(missing, field.name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("cannot %s: %s not stated", action, strings.Join(missing, ", "))
	}
	return nil
}
