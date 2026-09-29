package project

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/pkg/appbuild"
	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/configdoc"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
)

type Build struct {
	Dockerfile string
	Context    string
	Command    string
}

type Health struct {
	Path string
}

type Framework struct {
	Name     string
	Arch     string
	Detected bool
	Missing  error
}

func (r Framework) Architecture() string { return arch.Architecture(r.Arch) }

type App struct {
	Name              string
	Path              string
	Framework         Framework
	Entrypoint        string
	ProductionDomains []string
	Compute           string
	Build             *Build
	Health            *Health
	Folder            string
}

func (a App) RunsOn(compute provider.Compute) bool { return a.Compute == string(compute) }

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
	for _, a := range raw {
		if a.Name == "" {
			return nil, fmt.Errorf("app is missing required \"name\"")
		}
		if !validAppName(a.Name) {
			return nil, fmt.Errorf("invalid app name %q — an app name must be a DNS label: lowercase letters, digits and hyphens, 1–63 characters, not starting or ending with a hyphen. It is served as a label of a preview hostname (\"<preview>--%s.<your-preview-domain>\") and is a segment of every resource name this app deploys", a.Name, a.Name)
		}
		if a.Name == naming.InfraApp {
			return nil, fmt.Errorf("app name %q is reserved — it names the stack holding each environment's shared infrastructure, so no app may take it: rename the app", a.Name)
		}
		if seen[a.Name] {
			return nil, fmt.Errorf("duplicate app name %q — app names must be unique", a.Name)
		}
		seen[a.Name] = true

		if a.Path == "" {
			return nil, fmt.Errorf("app %q is missing required \"path\"", a.Name)
		}

		if a.Folder != "" {
			if err := variables.ValidateFolder(a.Folder); err != nil {
				return nil, fmt.Errorf("app %q: %w", a.Name, err)
			}
			if other, taken := boundFolders[a.Folder]; taken {
				return nil, fmt.Errorf("apps %q and %q both bind folder %q — a folder stores one app's values, so two apps sharing one would defeat the divergence folders exist for", other, a.Name, a.Folder)
			}
			boundFolders[a.Folder] = a.Name
		}

		var production configdoc.StringList
		if a.Domains != nil {
			production = a.Domains.Production
		}
		domains, err := normalizeProductionDomains(production, "")
		if err != nil {
			return nil, fmt.Errorf("app %q: %w", a.Name, err)
		}
		framework, err := resolveFramework(a.Name, filepath.Join(dir, filepath.FromSlash(a.Path)), a.Framework, a.Arch, a.Compute)
		if err != nil {
			return nil, err
		}
		build, err := normalizeBuild(a)
		if err != nil {
			return nil, err
		}
		health, err := normalizeHealth(a)
		if err != nil {
			return nil, err
		}
		apps = append(apps, App{
			Name:              a.Name,
			Path:              a.Path,
			Framework:         framework,
			Entrypoint:        a.Entrypoint,
			ProductionDomains: domains,
			Compute:           a.Compute,
			Build:             build,
			Health:            health,
			Folder:            a.Folder,
		})
	}

	return apps, nil
}

func rootApp(name, dir string) ([]App, error) {
	if !isRegularFile(filepath.Join(dir, nodeManifest)) {
		return nil, nil
	}
	framework := appbuild.FrameworkNode
	next, err := isNextApp(dir)
	if err != nil {
		return nil, err
	}
	if next {
		framework = appbuild.FrameworkNext
	}
	return []App{{Name: name, Path: ".", Framework: Framework{Name: framework, Detected: true}}}, nil
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
		return nil, fmt.Errorf("app %q sets health.path to %q, which is not a path off the app's root: give it one starting with %q, or drop health.path to have %q probed at %q", a.Name, a.Health.Path, "/", a.Name, "/")
	}
	if path != "" && !appbuild.HealthCheckPath(path) {
		return nil, fmt.Errorf("app %q sets health.path to %q, and a probe asks one path of the process: give %q a path containing no %q, %q, whitespace or control character, since a query or fragment names nothing the process is asked for", a.Name, a.Health.Path, a.Name, "?", "#")
	}
	return &Health{Path: path}, nil
}

func resolveFramework(app, dir, framework, declared, compute string) (Framework, error) {
	named := strings.TrimSpace(framework)
	compute = strings.TrimSpace(compute)
	name, err := frameworkOf(app, dir, named, compute)
	var missing error
	if err != nil && named == "" && compute == "" {
		missing, err = err, nil
	}
	if err != nil {
		return Framework{}, err
	}
	architecture, err := architectureOf(app, strings.TrimSpace(declared))
	if err != nil {
		return Framework{}, err
	}
	return Framework{Name: name, Arch: architecture, Detected: named == "" && name != "", Missing: missing}, nil
}
