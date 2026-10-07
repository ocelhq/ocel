package manifest

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/attribution"
	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/english"
	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/workspace"
	"github.com/ocelhq/ocel/pkg/naming"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

type usage struct {
	Type  resourcesv1.ResourceType
	Name  string
	Files []string
}

type DanglingUsageError struct {
	App  string
	Type resourcesv1.ResourceType
	Name string
}

func (e *DanglingUsageError) Error() string {
	return fmt.Sprintf(
		"app %q is attributed %s %q, which nothing in this project declares",
		e.App, e.Type, e.Name,
	)
}

func FindUsages(ctx context.Context, cfg *project.Project, resources []declaration.Resource) ([]attribution.Usage, error) {
	apps, err := attributionApps(cfg)
	if err != nil {
		return nil, err
	}
	declared := make([]attribution.Declaration, 0, len(resources))
	for _, r := range resources {
		if _, bindable := naming.BindableAs(r.Type); bindable {
			declared = append(declared, attribution.Declaration{Type: r.Type, Name: r.Name, Source: r.Source})
		}
	}
	return attribution.FindUsages(ctx, cfg.Dir, apps, declared)
}

func attributionApps(cfg *project.Project) ([]attribution.App, error) {
	roots, err := discovery.RootsOf(cfg)
	if err != nil {
		return nil, err
	}
	out := make([]attribution.App, 0, len(cfg.Apps))
	for _, a := range cfg.Apps {
		inAnImage := a.RunsOn(provider.ComputeContainer)
		appDir := filepath.Join(cfg.Dir, a.Path)
		out = append(out, attribution.App{
			Name:      a.Name,
			Path:      a.Path,
			Language:  language.OfApp(a.Framework(), appDir),
			Roots:     roots,
			Container: inAnImage,
			Members:   workspaceMembers(inAnImage, appDir),
		})
	}
	return out, nil
}

func RefuseUnnamedApps(cfg *project.Project, built build.Output) error {
	var unnamed []string
	for _, name := range detectedApps(servedByFunctions(built.Functions, cfg)) {
		if !slices.ContainsFunc(cfg.Apps, func(a project.App) bool { return a.Name == name }) {
			unnamed = append(unnamed, name)
		}
	}
	if len(unnamed) > 0 {
		return fmt.Errorf(
			"this project builds %s, which `apps` in %s does not name: ocel reads a named app's source to tell which resources it may be handed, and refuses to deploy an app it can attribute nothing to — give each one a name and a path under `apps`",
			english.And(english.Quoted(unnamed)), filepath.Base(cfg.Path),
		)
	}
	return nil
}

func detectedApps(functions []build.Function) []string {
	var detected []string
	for _, f := range functions {
		if f.App != "" && !slices.Contains(detected, f.App) {
			detected = append(detected, f.App)
		}
	}
	slices.Sort(detected)
	return detected
}

func workspaceMembers(inAnImage bool, appDir string) []string {
	if !inAnImage {
		return nil
	}
	located, err := workspace.Locate(appDir)
	if err != nil {
		return nil
	}
	return located.Members()
}

func manifestUsages(apps []app, declared map[identity]declaredResource) ([]*contractv1.ManifestUsage, error) {
	merged := map[string]*contractv1.ManifestUsage{}
	for _, a := range apps {
		for _, u := range a.Usages {
			if _, ok := declared[identity{u.Type, u.Name}]; !ok {
				return nil, &DanglingUsageError{App: a.Name, Type: u.Type, Name: u.Name}
			}
			kind, err := typeKind(u.Type)
			if err != nil {
				return nil, err
			}

			logical := resourceLogicalName(kind, u.Name)
			edge, ok := merged[a.Name+naming.KeySeparator+logical]
			if !ok {
				edge = &contractv1.ManifestUsage{App: a.Name, Resource: logical}
				merged[a.Name+naming.KeySeparator+logical] = edge
			}
			for _, f := range u.Files {
				if !slices.Contains(edge.Files, f) {
					edge.Files = append(edge.Files, f)
				}
			}
		}
	}
	if len(merged) == 0 {
		return nil, nil
	}

	out := make([]*contractv1.ManifestUsage, 0, len(merged))
	for _, edge := range merged {
		slices.Sort(edge.Files)
		out = append(out, edge)
	}
	slices.SortFunc(out, func(a, b *contractv1.ManifestUsage) int {
		if c := strings.Compare(a.GetApp(), b.GetApp()); c != 0 {
			return c
		}
		return strings.Compare(a.GetResource(), b.GetResource())
	})
	return out, nil
}
