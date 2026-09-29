package manifest

import (
	"cmp"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/attribution"
	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/cli/internal/variablescope"
	"github.com/ocelhq/ocel/pkg/appbuild"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

type app struct {
	Name            string
	Framework       appbuild.Framework
	ClientBundle    bool
	Compute         string
	Domains         []string
	Folder          string
	Usages          []usage
	Image           string
	HealthCheckPath string
}

func appsOf(projectDir string, configured []project.App, usages []attribution.Usage, compute string, images map[string]string, functions []build.Function) []app {
	byApp := make(map[string][]usage, len(configured))
	for _, u := range usages {
		byApp[u.App] = append(byApp[u.App], usage{Type: u.Type, Name: u.Name, Files: u.Files})
	}

	out := make([]app, 0, len(configured))
	named := make(map[string]bool, len(configured))
	for _, a := range configured {
		named[a.Name] = true
		out = append(out, app{
			Name:            a.Name,
			Framework:       appbuild.Framework{Name: a.Framework.Name, Arch: a.Framework.Arch},
			ClientBundle:    language.HasClientBundle(a.Framework.Name, filepath.Join(projectDir, a.Path)),
			Compute:         a.Compute,
			Domains:         a.ProductionDomains,
			Folder:          a.Folder,
			Usages:          byApp[a.Name],
			Image:           images[a.Name],
			HealthCheckPath: healthPathOf(a),
		})
	}
	for _, name := range slices.Sorted(maps.Keys(byApp)) {
		if !named[name] {
			framework := unnamedFramework(name, functions)
			out = append(out, app{
				Name:         name,
				Framework:    framework,
				ClientBundle: appbuild.FrameworkBundlesClient(framework.Name),
				Compute:      compute,
				Usages:       byApp[name],
			})
		}
	}
	return out
}

func unnamedFramework(app string, functions []build.Function) appbuild.Framework {
	for _, f := range functions {
		if f.App == app && f.Framework.Name != "" {
			return f.Framework
		}
	}
	return appbuild.Framework{Name: appbuild.FrameworkNode}
}

func healthPathOf(app project.App) string {
	if app.Health == nil {
		return ""
	}
	return app.Health.Path
}

func servedByFunctions(functions []build.Function, cfg *project.Project) []build.Function {
	containers := make(map[string]bool, len(cfg.Apps))
	for _, a := range build.ImageApps(cfg.Apps) {
		containers[a.Name] = true
	}
	if len(containers) == 0 {
		return functions
	}
	return slices.DeleteFunc(slices.Clone(functions), func(f build.Function) bool {
		return containers[f.App]
	})
}

func variablesByApp(values map[string][]variables.Variable, functions []build.Function) map[string][]variables.Variable {
	root, ok := values[variablescope.RootApp]
	if !ok {
		return values
	}
	byApp := make(map[string][]variables.Variable, len(functions))
	for _, f := range functions {
		byApp[f.App] = root
	}
	return byApp
}

func manifestAppsOf(apps []app, compute string, functions []build.Function, functionsByApp map[string][]*contractv1.ManifestFunction, values map[string][]variables.Variable) ([]*contractv1.ManifestApp, error) {
	frameworkByApp := make(map[string]appbuild.Framework, len(functions))
	for _, f := range functions {
		if f.App != "" && f.Framework.Name != "" {
			if _, ok := frameworkByApp[f.App]; !ok {
				frameworkByApp[f.App] = f.Framework
			}
		}
	}

	manifestApps := make([]*contractv1.ManifestApp, 0, len(apps))
	configured := make(map[string]bool, len(apps))
	for _, a := range apps {
		configured[a.Name] = true
		framework := a.Framework
		if framework.Name == "" {
			framework = frameworkByApp[a.Name]
		}
		manifestApp := &contractv1.ManifestApp{
			Name:         a.Name,
			Framework:    frameworkProto(framework.Name, framework.Arch),
			Domains:      tierDomains(project.Domains{Production: a.Domains}),
			Variables:    manifestVariables(values[a.Name]),
			Folder:       a.Folder,
			ClientBundle: a.ClientBundle,
		}
		if err := attachArtifact(manifestApp, a, cmp.Or(a.Compute, compute), functionsByApp[a.Name]); err != nil {
			return nil, err
		}
		manifestApps = append(manifestApps, manifestApp)
	}

	for _, f := range functions {
		if f.App == "" || configured[f.App] {
			continue
		}
		configured[f.App] = true
		if compute == string(provider.ComputeContainer) {
			return nil, fmt.Errorf("app %q runs on container compute and this project's config does not name it, so there is no directory to build its image from: give %q a name and a path under `apps`", f.App, f.App)
		}
		manifestApps = append(manifestApps, &contractv1.ManifestApp{
			Name:         f.App,
			Framework:    frameworkProto(frameworkByApp[f.App].Name, frameworkByApp[f.App].Arch),
			Artifact:     serverlessArtifact(functionsByApp[f.App]),
			Variables:    manifestVariables(values[f.App]),
			ClientBundle: appbuild.FrameworkBundlesClient(frameworkByApp[f.App].Name),
		})
	}

	slices.SortFunc(manifestApps, func(a, b *contractv1.ManifestApp) int { return strings.Compare(a.GetName(), b.GetName()) })
	return manifestApps, nil
}

func manifestVariables(values []variables.Variable) []*contractv1.ManifestVariable {
	if len(values) == 0 {
		return nil
	}
	out := make([]*contractv1.ManifestVariable, 0, len(values))
	for _, v := range values {
		out = append(out, &contractv1.ManifestVariable{Key: v.Key, Class: v.Class, Value: v.Value, Folder: v.Folder, Version: v.Version, Description: v.Description})
	}
	slices.SortFunc(out, func(a, b *contractv1.ManifestVariable) int { return strings.Compare(a.GetKey(), b.GetKey()) })
	return out
}

func tierDomains(domains project.Domains) []*contractv1.TierDomains {
	var out []*contractv1.TierDomains
	if domains.Preview != "" {
		out = append(out, &contractv1.TierDomains{Tier: environmentv1.Tier_TIER_PREVIEW, Hostnames: []string{domains.Preview}})
	}
	if len(domains.Production) > 0 {
		out = append(out, &contractv1.TierDomains{Tier: environmentv1.Tier_TIER_PRODUCTION, Hostnames: domains.Production})
	}
	return out
}
