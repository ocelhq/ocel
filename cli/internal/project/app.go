package project

import (
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/buildoutput"
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

	BuildsWithResources bool
}

type Serverless struct {
	Framework  string
	Entrypoint string
}

type Container struct {
	Build        *Build
	Health       *Health
	MinInstances *int
	MaxInstances *int
	Framework    string
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
	if a.Serverless != nil {
		return a.Serverless.Framework
	}
	if a.Container != nil {
		return a.Container.Framework
	}
	return ""
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

			BuildsWithResources: a.BuildWithResources == nil || *a.BuildWithResources,
		}
		if err := shapeApp(&app, a, filepath.Join(dir, filepath.FromSlash(a.Path))); err != nil {
			return nil, err
		}
		apps = append(apps, app)
	}

	return apps, nil
}

func shapeApp(app *App, a configdoc.AppConfig, dir string) error {
	var compute configdoc.ComputeDescriptor
	if a.Compute != nil {
		compute = *a.Compute
	}
	switch {
	case compute.Container != nil:
		app.Compute = provider.ComputeContainer
		container, err := normalizeContainer(a.Name, compute.Container)
		if err != nil {
			return newInvalidConfigError(err, "")
		}
		if isDir(dir) {
			framework, _, err := language.DetectFramework(dir)
			if err != nil {
				return fmt.Errorf("app %q: %w", a.Name, err)
			}
			if framework == buildoutput.FrameworkNext {
				container.Framework = framework
			}
		}
		app.Container = container
	case compute.Serverless != nil:
		app.Compute = provider.ComputeServerless
		named := strings.TrimSpace(compute.Serverless.Framework)
		framework, err := frameworkOf(a.Name, dir, named)
		if err != nil {
			return err
		}
		app.Serverless = &Serverless{Framework: framework, Entrypoint: compute.Serverless.Entrypoint}
	default:
		framework, err := frameworkOf(a.Name, dir, "")
		switch {
		case err != nil:
			app.undetected = err
		case framework != "":
			app.Serverless = &Serverless{Framework: framework}
		}
		app.Container = &Container{}
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
		Serverless: &Serverless{Framework: framework},
		Container:  &Container{},

		BuildsWithResources: true,
	}}, nil
}

func normalizeContainer(app string, raw *configdoc.ContainerCompute) (*Container, error) {
	build, err := normalizeImage(app, raw.Image)
	if err != nil {
		return nil, err
	}
	health, err := normalizeHealth(app, raw.Health)
	if err != nil {
		return nil, err
	}
	container := &Container{Build: build, Health: health}
	if raw.Instances != nil {
		container.MinInstances, container.MaxInstances = raw.Instances.Min, raw.Instances.Max
	}
	if err := refuseImpossibleInstances(app, container); err != nil {
		return nil, err
	}
	return container, nil
}

func normalizeImage(app string, image *configdoc.ImageConfig) (*Build, error) {
	if image == nil || image.Dockerfile == "" && image.Context == "" && image.Command == "" {
		return nil, nil
	}
	dockerfile := strings.TrimSpace(image.Dockerfile)
	if dockerfile == "" && image.Dockerfile != "" {
		return nil, fmt.Errorf("app %q sets image.dockerfile to %q, which names no file: give it the path to a Dockerfile, or drop image.dockerfile to build %q from the Dockerfile beside it or from no configuration at all", app, image.Dockerfile, app)
	}
	context := strings.TrimSpace(image.Context)
	if context == "" && image.Context != "" {
		return nil, fmt.Errorf("app %q sets image.context to %q, which names no directory: give it the directory the image is built from, or drop image.context to build %q from the workspace root its own directory sits in", app, image.Context, app)
	}
	command := strings.TrimSpace(image.Command)
	if command == "" && image.Command != "" {
		return nil, fmt.Errorf("app %q sets image.command to %q, which names no command: give it the command that builds %q inside the image, or drop image.command to run the app's own build script", app, image.Command, app)
	}
	return &Build{Dockerfile: dockerfile, Context: context, Command: command}, nil
}

func normalizeHealth(app string, health *configdoc.HealthConfig) (*Health, error) {
	if health == nil {
		return nil, nil
	}
	path := strings.TrimSpace(health.Path)
	if path != "" && !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("app %q sets health.path to %q, which is not a path off the app's root: give it one starting with %q, or drop health.path to have the provider choose %q's health check", app, health.Path, "/", app)
	}
	if path != "" && !containerimage.IsHealthCheckPath(path) {
		return nil, fmt.Errorf("app %q sets health.path to %q, and a probe asks one path of the process: give %q a path containing no %q, %q, whitespace or control character, since a query or fragment names nothing the process is asked for", app, health.Path, app, "?", "#")
	}
	return &Health{Path: path}, nil
}

func refuseImpossibleInstances(app string, container *Container) error {
	for key, count := range map[string]*int{"instances.min": container.MinInstances, "instances.max": container.MaxInstances} {
		if count != nil && *count > math.MaxInt32 {
			return fmt.Errorf("app %q sets %s %d, and no provider runs more than %d instances of an app: lower it", app, key, *count, math.MaxInt32)
		}
	}
	if container.MinInstances == nil || container.MaxInstances == nil || *container.MinInstances <= *container.MaxInstances {
		return nil
	}
	return fmt.Errorf(
		"app %q sets instances.min %d and instances.max %d, and an app cannot keep more instances running than it may run at once: raise instances.max to at least %d, or lower instances.min",
		app, *container.MinInstances, *container.MaxInstances, *container.MinInstances,
	)
}
