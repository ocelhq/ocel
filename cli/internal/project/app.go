package project

import (
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/english"
	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/configdoc"
	"github.com/ocelhq/ocel/pkg/containerimage"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
)

type App struct {
	Name              string
	Path              string
	Folder            string
	Arch              string
	ProductionDomains []string
	Compute           provider.Compute
	Serverless        *Serverless
	Container         *Container
	undetected        error
}

type Serverless struct {
	Framework  string
	Detected   bool
	Entrypoint string
}

type Container struct {
	Build        *Build
	Health       *Health
	MinInstances *int
	MaxInstances *int
}

func (c *Container) Instances() provider.Instances {
	instances := provider.Instances{Min: 1}
	if c.MinInstances != nil {
		instances.Min = *c.MinInstances
	}
	instances.Max = max(instances.Min, 1)
	if c.MaxInstances != nil {
		instances.Max = *c.MaxInstances
	}
	return instances
}

func (c *Container) namesInstances() bool {
	return c.MinInstances != nil || c.MaxInstances != nil
}

type Build struct {
	Dockerfile string
	Context    string
	Command    string
}

type Health struct {
	Path string
}

func (a App) RunsOn(compute provider.Compute) bool { return a.Compute == compute }

func (a App) Framework() string {
	if a.Serverless == nil {
		return ""
	}
	return a.Serverless.Framework
}

func (a App) Architecture() string { return arch.Architecture(a.Arch) }

func (p *Project) HasJSApp() bool {
	return slices.ContainsFunc(p.Apps, func(a App) bool {
		return language.OfApp(a.Framework(), filepath.Join(p.Dir, a.Path)) == language.JS
	})
}

func SharedFolder(apps []App) string {
	if len(apps) == 0 {
		return ""
	}
	folder := apps[0].Folder
	for _, app := range apps[1:] {
		if app.Folder != folder {
			return ""
		}
	}
	return folder
}

var dnsLabelPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func validAppName(name string) bool {
	return dnsLabelPattern.MatchString(name)
}

func normalizeApps(raw []configdoc.AppConfig, dir string) ([]App, error) {
	if len(raw) == 0 {
		return nil, nil
	}

	apps := make([]App, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	boundFolders := make(map[string]string, len(raw))
	for i, a := range raw {
		field := func(key string) string { return configdoc.JoinPath(configdoc.IndexPath("apps", i), key) }
		if a.Name == "" {
			return nil, newInvalidConfigError(fmt.Errorf("app is missing required \"name\""), field("name"))
		}
		if !validAppName(a.Name) {
			return nil, newInvalidConfigError(fmt.Errorf("invalid app name %q — an app name must be a DNS label: lowercase letters, digits and hyphens, 1–63 characters, not starting or ending with a hyphen. It is served as a label of a preview hostname (\"<preview>--%s.<your-preview-domain>\") and is a segment of every resource name this app deploys", a.Name, a.Name), field("name"))
		}
		if a.Name == naming.InfraApp {
			return nil, newInvalidConfigError(fmt.Errorf("app name %q is reserved — it names the stack holding each environment's shared infrastructure, so no app may take it: rename the app", a.Name), field("name"))
		}
		if seen[a.Name] {
			return nil, newInvalidConfigError(fmt.Errorf("duplicate app name %q — app names must be unique", a.Name), field("name"))
		}
		seen[a.Name] = true

		if a.Path == "" {
			return nil, newInvalidConfigError(fmt.Errorf("app %q is missing required \"path\"", a.Name), field("path"))
		}

		if a.Folder != "" {
			if err := variables.ValidateFolder(a.Folder); err != nil {
				return nil, newInvalidConfigError(fmt.Errorf("app %q: %w", a.Name, err), field("folder"))
			}
			if other, taken := boundFolders[a.Folder]; taken {
				return nil, newInvalidConfigError(fmt.Errorf("apps %q and %q both bind folder %q — a folder stores one app's values, so two apps sharing one would defeat the divergence folders exist for", other, a.Name, a.Folder), field("folder"))
			}
			boundFolders[a.Folder] = a.Name
		}

		var production configdoc.StringList
		if a.Domains != nil {
			production = a.Domains.Production
		}
		domains, err := normalizeProductionDomains(production, "")
		if err != nil {
			return nil, newInvalidConfigError(fmt.Errorf("app %q: %w", a.Name, err), field("domains.production"))
		}
		architecture, err := architectureOf(a.Name, strings.TrimSpace(a.Arch))
		if err != nil {
			return nil, newInvalidConfigError(err, field("arch"))
		}
		app := App{
			Name:              a.Name,
			Path:              a.Path,
			Folder:            a.Folder,
			Arch:              architecture,
			ProductionDomains: domains,
		}
		if err := shapeApp(&app, a, filepath.Join(dir, filepath.FromSlash(a.Path))); err != nil {
			return nil, err
		}
		apps = append(apps, app)
	}

	return apps, nil
}

func shapeApp(app *App, a configdoc.AppConfig, dir string) error {
	compute := provider.Compute(strings.TrimSpace(a.Compute))
	if compute != "" && !provider.KnownCompute(string(compute)) {
		return newInvalidConfigError(fmt.Errorf("app %q asks for compute %q, which ocel does not know: the computes are %s", a.Name, compute, english.Or(english.Quoted(provider.ComputeNames(provider.Computes())))), "")
	}
	app.Compute = compute

	build, err := normalizeBuild(a)
	if err != nil {
		return newInvalidConfigError(err, "")
	}
	health, err := normalizeHealth(a)
	if err != nil {
		return newInvalidConfigError(err, "")
	}
	container := &Container{Build: build, Health: health, MinInstances: a.MinInstances, MaxInstances: a.MaxInstances}
	if err := refuseImpossibleInstances(a.Name, container); err != nil {
		return newInvalidConfigError(err, "")
	}
	if compute != provider.ComputeServerless {
		app.Container = container
	}

	named := strings.TrimSpace(a.Framework)
	if compute == provider.ComputeContainer {
		if named != "" {
			return newInvalidConfigError(frameworkOnContainer(a.Name, named, compute), "")
		}
		return nil
	}

	framework, err := frameworkOf(a.Name, dir, named)
	switch {
	case err != nil && compute == "" && named == "":
		app.undetected = err
	case err != nil:
		return err
	case framework != "":
		app.Serverless = &Serverless{Framework: framework, Detected: named == "", Entrypoint: a.Entrypoint}
	}
	if compute == provider.ComputeServerless {
		if err := refuseContainerConfig(*app, compute, container); err != nil {
			return newInvalidConfigError(err, "")
		}
	}
	return nil
}

func rootApp(name, dir string) ([]App, error) {
	framework, found, err := language.DetectFramework(dir)
	if err != nil || !found || language.OfApp(framework, dir) != language.JS {
		return nil, err
	}
	return []App{{
		Name:       name,
		Path:       ".",
		Serverless: &Serverless{Framework: framework, Detected: true},
		Container:  &Container{},
	}}, nil
}

func normalizeBuild(a configdoc.AppConfig) (*Build, error) {
	if a.Build == nil {
		return nil, nil
	}
	dockerfile := strings.TrimSpace(a.Build.Dockerfile)
	if dockerfile == "" && a.Build.Dockerfile != "" {
		return nil, fmt.Errorf("app %q sets build.dockerfile to %q, which names no file: give it the path to a Dockerfile, or drop build.dockerfile to build %q from the Dockerfile beside it or from no configuration at all", a.Name, a.Build.Dockerfile, a.Name)
	}
	context := strings.TrimSpace(a.Build.Context)
	if context == "" && a.Build.Context != "" {
		return nil, fmt.Errorf("app %q sets build.context to %q, which names no directory: give it the directory the image is built from, or drop build.context to build %q from the workspace root its own directory sits in", a.Name, a.Build.Context, a.Name)
	}
	command := strings.TrimSpace(a.Build.Command)
	if command == "" && a.Build.Command != "" {
		return nil, fmt.Errorf("app %q sets build.command to %q, which names no command: give it the command that builds %q inside the image, or drop build.command to run the app's own build script", a.Name, a.Build.Command, a.Name)
	}
	return &Build{Dockerfile: dockerfile, Context: context, Command: command}, nil
}

func normalizeHealth(a configdoc.AppConfig) (*Health, error) {
	if a.Health == nil {
		return nil, nil
	}
	path := strings.TrimSpace(a.Health.Path)
	if path != "" && !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("app %q sets health.path to %q, which is not a path off the app's root: give it one starting with %q, or drop health.path to have the provider choose %q's health check", a.Name, a.Health.Path, "/", a.Name)
	}
	if path != "" && !containerimage.IsHealthCheckPath(path) {
		return nil, fmt.Errorf("app %q sets health.path to %q, and a probe asks one path of the process: give %q a path containing no %q, %q, whitespace or control character, since a query or fragment names nothing the process is asked for", a.Name, a.Health.Path, a.Name, "?", "#")
	}
	return &Health{Path: path}, nil
}

func frameworkOnContainer(app, framework string, compute provider.Compute) error {
	return fmt.Errorf(
		"app %q declares framework %q, and it runs on %q compute, which runs the image it is given: a framework names what a serverless app's functions are built with and nothing else — give %q `compute: \"serverless\"`, or remove its `framework`",
		app, framework, compute, app,
	)
}

func refuseImpossibleInstances(app string, container *Container) error {
	for key, count := range map[string]*int{"minInstances": container.MinInstances, "maxInstances": container.MaxInstances} {
		if count != nil && *count > math.MaxInt32 {
			return fmt.Errorf("app %q sets %s %d, and no provider runs more than %d instances of an app: lower it", app, key, *count, math.MaxInt32)
		}
	}
	if container.MinInstances == nil || container.MaxInstances == nil || *container.MinInstances <= *container.MaxInstances {
		return nil
	}
	return fmt.Errorf(
		"app %q sets minInstances %d and maxInstances %d, and an app cannot keep more instances running than it may run at once: raise maxInstances to at least %d, or lower minInstances",
		app, *container.MinInstances, *container.MaxInstances, *container.MinInstances,
	)
}

func refuseContainerConfig(app App, compute provider.Compute, container *Container) error {
	build, health := container.Build, container.Health
	if build != nil {
		return fmt.Errorf(
			"app %q configures a `build`, and it runs on %q compute, which builds no image: `build` configures a container image and nothing else — give %q `compute: \"container\"`, or remove its `build`",
			app.Name, compute, app.Name,
		)
	}
	if health != nil {
		return fmt.Errorf(
			"app %q configures a `health` check, and it runs on %q compute, which runs no process to probe: `health` gates a container release and nothing else — give %q `compute: \"container\"`, or remove its `health`",
			app.Name, compute, app.Name,
		)
	}
	if container.namesInstances() {
		key := "minInstances"
		if container.MinInstances == nil {
			key = "maxInstances"
		}
		return fmt.Errorf(
			"app %q sets `%s`, and it runs on %q compute, which scales itself: instance counts size a container app and nothing else — give %q `compute: \"container\"`, or remove its `minInstances` and `maxInstances`",
			app.Name, key, compute, app.Name,
		)
	}
	return nil
}
