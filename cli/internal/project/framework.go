package project

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/english"
	"github.com/ocelhq/ocel/cli/internal/gomodule"
	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/cli/internal/pythonserver"
	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/configdoc"
	"github.com/ocelhq/ocel/pkg/provider"
)

func resolveFramework(app *App, a configdoc.AppConfig, dir, root string, field func(string) string) error {
	named := strings.TrimSpace(a.Framework)
	if named != "" && !buildoutput.IsKnownFramework(named) {
		return newInvalidConfigError(fmt.Errorf("app %q declares framework %q, which nothing builds: the frameworks are %s", a.Name, named, english.And(english.Quoted(buildoutput.Frameworks()))), field("framework"))
	}
	var resolved language.Detection
	var languages []language.Language
	if named != "" || isDir(dir) {
		detected, err := language.Resolve(dir, root, named)
		var undetected *language.UndetectedAppError
		switch {
		case named == "" && errors.As(err, &undetected):
			frameworks := english.Or(english.Quoted(buildoutput.Frameworks()))
			if err := app.refuseServerless(newInvalidConfigError(fmt.Errorf("app %q: %w: set \"framework\" to one of %s", a.Name, err, frameworks), field("framework"))); err != nil {
				return err
			}
			resolved = language.Detection{Language: undetected.Language(), GoModule: undetected.GoModule()}
			languages = undetected.Languages()
		case err != nil:
			return newInvalidConfigError(fmt.Errorf("app %q sets framework %q, and %w", a.Name, named, err), field("framework"))
		default:
			resolved, languages = detected, []language.Language{detected.Language}
		}
	}
	return app.resolve(resolved, languages, a.Entrypoint, dir, field)
}

func (a *App) resolve(resolved language.Detection, languages []language.Language, declaredEntrypoint, dir string, field func(string) string) error {
	a.Framework, a.Language, a.Languages, a.Entrypoint = resolved.Framework, resolved.Language, languages, resolved.Entrypoint
	a.FrameworkSource, a.Evidence, a.GoModule = resolved.Source, resolved.Evidence, resolved.GoModule
	if declaredEntrypoint != "" {
		a.Entrypoint = declaredEntrypoint
	}
	if err := refuseDeclaredEntrypoint(a.Name, a.Framework, declaredEntrypoint, field); err != nil {
		return err
	}
	if !isDir(dir) {
		return nil
	}
	if a.GoModule != nil {
		pkg, err := gomodule.PackagePath(a.GoModule.Dir, dir, a.Entrypoint)
		if err != nil {
			return newInvalidConfigError(fmt.Errorf("app %q: %w", a.Name, err), field("entrypoint"))
		}
		a.GoPackage = pkg
	}
	return a.refuseUnserved(resolved.Lacking, dir, field)
}

func refuseDeclaredEntrypoint(app, framework, entrypoint string, field func(string) string) error {
	if entrypoint == "" {
		return nil
	}
	if buildoutput.BuildsWithItsOwnScript(framework) {
		return newInvalidConfigError(fmt.Errorf("app %q is built with %s and sets entrypoint %q: %s builds from its own config, so drop \"entrypoint\"", app, framework, entrypoint, framework), field("entrypoint"))
	}
	if err := refuseEntrypointForm(app, framework, entrypoint); err != nil {
		return newInvalidConfigError(err, field("entrypoint"))
	}
	return nil
}

func (a *App) refuseUnserved(lacking, dir string, field func(string) string) error {
	switch a.Framework {
	case buildoutput.FrameworkPython:
		if a.Entrypoint == "" {
			return a.refuseServerless(newInvalidConfigError(fmt.Errorf("app %q is built with python, and %s: set \"entrypoint\" to the file or module that serves it", a.Name, lacking), field("entrypoint")))
		}
	case buildoutput.FrameworkNode:
		if a.Entrypoint == "" {
			return a.refuseServerless(newInvalidConfigError(fmt.Errorf("app %q is built with node, and %s: set \"entrypoint\" to the file its function serves", a.Name, lacking), field("entrypoint")))
		}
	case buildoutput.FrameworkGo:
		if a.GoModule == nil {
			return a.refuseServerless(newInvalidConfigError(fmt.Errorf("app %q is built with go, and neither %s nor a directory above it, up to the project root, has a %s", a.Name, dir, gomodule.File), field("framework")))
		}
	}
	return nil
}

func (a *App) refuseServerless(refusal error) error {
	if a.Compute == provider.ComputeServerless {
		return refusal
	}
	a.serverlessRefusal = refusal
	return nil
}

var cargoTargetName = regexp.MustCompile(`^[\p{L}\p{N}_-]+$`)

func refuseEntrypointForm(app, framework, entrypoint string) error {
	switch framework {
	case buildoutput.FrameworkRust:
		if !cargoTargetName.MatchString(entrypoint) {
			return fmt.Errorf("app %q is built with rust and sets entrypoint %q, which names no binary: give it one of the crate's binaries, as cargo run --bin takes it", app, entrypoint)
		}
	case buildoutput.FrameworkPython:
		if _, err := pythonserver.ParseEntrypoint(entrypoint); err != nil {
			return fmt.Errorf("app %q is built with python, and %w", app, err)
		}
	case buildoutput.FrameworkGo:
		if !filepath.IsLocal(filepath.FromSlash(entrypoint)) {
			return fmt.Errorf("app %q is built with go and sets entrypoint %q, which names no directory inside the app: give it the main package's directory, relative to the app", app, entrypoint)
		}
	}
	return nil
}

func architectureOf(app string, declared string) (string, error) {
	if declared == "" || declared == arch.X8664 || declared == arch.ARM64 {
		return declared, nil
	}
	return "", fmt.Errorf("app %q declares arch %q, which names no architecture: the architectures are %q and %q", app, declared, arch.X8664, arch.ARM64)
}
