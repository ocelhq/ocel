package manifest

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/attribution"
	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

type app struct {
	Name            string
	Framework       string
	Arch            string
	ClientBundle    bool
	Compute         provider.Compute
	Domains         []string
	Folder          string
	Usages          []usage
	Image           string
	HealthCheckPath string
	Instances       provider.Instances
}

func appsOf(projectDir string, configured []project.App, usages []attribution.Usage, images map[string]string) []app {
	byApp := make(map[string][]usage, len(configured))
	for _, u := range usages {
		byApp[u.App] = append(byApp[u.App], usage{Type: u.Type, Name: u.Name, Files: u.Files})
	}

	out := make([]app, 0, len(configured))
	for _, a := range configured {
		out = append(out, app{
			Name:            a.Name,
			Framework:       a.Framework(),
			Arch:            a.Arch,
			ClientBundle:    language.HasClientBundle(a.Framework(), filepath.Join(projectDir, a.Path)),
			Compute:         a.Compute,
			Domains:         a.ProductionDomains,
			Folder:          a.Folder,
			Usages:          byApp[a.Name],
			Image:           images[a.Name],
			HealthCheckPath: healthPathOf(a),
			Instances:       instancesOf(a),
		})
	}
	return out
}

func healthPathOf(app project.App) string {
	if app.Container == nil || app.Container.Health == nil {
		return ""
	}
	return app.Container.Health.Path
}

func instancesOf(app project.App) provider.Instances {
	if app.Container == nil {
		return provider.Instances{}
	}
	return app.Container.Instances()
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

func manifestAppsOf(apps []app, functions []build.Function, functionsByApp map[string][]*contractv1.ManifestFunction, values map[string][]variables.Variable) ([]*contractv1.ManifestApp, error) {
	frameworkByApp := make(map[string]buildoutput.Framework, len(functions))
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
		if a.Compute == "" {
			return nil, fmt.Errorf("app %q reached the manifest with no compute resolved — every app on the wire names the compute it runs on, and the manifest is built after preflight so that a provider's own answer is what fills it", a.Name)
		}
		framework := buildoutput.Framework{Name: a.Framework, Arch: a.Arch}
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
		if err := attachArtifact(manifestApp, a, functionsByApp[a.Name]); err != nil {
			return nil, err
		}
		manifestApps = append(manifestApps, manifestApp)
	}

	for _, f := range functions {
		if !configured[f.App] {
			return nil, fmt.Errorf("the build output holds functions of app %q, which this project's config does not name: run `ocel build` again, or give %q a name and a path under `apps`", f.App, f.App)
		}
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
