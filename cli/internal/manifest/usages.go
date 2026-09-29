package manifest

import (
	"cmp"
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/attribution"
	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/discovery"
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

func FindUsages(ctx context.Context, cfg *project.Project, built build.Output, compute string, resources []declaration.Resource) ([]attribution.Usage, error) {
	apps, err := attributionApps(cfg, servedByFunctions(built.Functions, cfg), compute)
	if err != nil {
		return nil, err
	}
	declared := make([]attribution.Declaration, len(resources))
	for i, r := range resources {
		declared[i] = attribution.Declaration{Type: r.Type, Name: r.Name, Source: r.Source}
	}
	return attribution.FindUsages(ctx, cfg.Dir, apps, declared)
}

func attributionApps(cfg *project.Project, functions []build.Function, compute string) ([]attribution.App, error) {
	configName := filepath.Base(cfg.Path)
	detected := detectedApps(functions)
	roots, err := discovery.RootsOf(cfg)
	if err != nil {
		return nil, err
	}
	container := compute == string(provider.ComputeContainer)

	if len(cfg.Apps) == 0 {
		if len(detected) > 1 {
			return nil, fmt.Errorf(
				"this project builds %d apps (%s) but names none of them, so ocel cannot tell which source belongs to which and refuses to hand every app every resource: give each one a name and a path under `apps` in %s",
				len(detected), strings.Join(detected, ", "), configName,
			)
		}
		out := make([]attribution.App, 0, len(detected))
		for _, name := range detected {
			out = append(out, attribution.App{
				Name:      name,
				Path:      ".",
				Language:  language.OfApp("", cfg.Dir),
				Roots:     roots,
				Container: container,
				Members:   workspaceMembers(container, cfg.Dir),
			})
		}
		return out, nil
	}

	named := make(map[string]bool, len(cfg.Apps))
	out := make([]attribution.App, 0, len(cfg.Apps))
	for _, a := range cfg.Apps {
		named[a.Name] = true
		inAnImage := cmp.Or(a.Compute, compute) == string(provider.ComputeContainer)
		appDir := filepath.Join(cfg.Dir, a.Path)
		out = append(out, attribution.App{
			Name:      a.Name,
			Path:      a.Path,
			Language:  language.OfApp(a.Framework.Name, appDir),
			Roots:     roots,
			Container: inAnImage,
			Members:   workspaceMembers(inAnImage, appDir),
		})
	}

	var unnamed []string
	for _, name := range detected {
		if !named[name] {
			unnamed = append(unnamed, name)
		}
	}
	if len(unnamed) > 0 {
		return nil, fmt.Errorf(
			"this project builds %s, which `apps` in %s does not name: ocel reads a named app's source to tell which resources it may be handed, and refuses to deploy an app it can attribute nothing to — give each one a name and a path under `apps`",
			strings.Join(unnamed, ", "), configName,
		)
	}
	return out, nil
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
