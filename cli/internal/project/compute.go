package project

import (
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/cli/internal/english"
	"github.com/ocelhq/ocel/pkg/provider"
)

func (p *Project) ResolveComputes(runs []string, vendor string) (*Project, error) {
	if len(runs) == 0 {
		return nil, fmt.Errorf(
			"%s names no compute it runs, so there is nothing for this project's apps to run on: a provider must name at least one, and ocel will not guess one for it",
			vendor,
		)
	}
	computes := make([]provider.Compute, 0, len(runs))
	for _, name := range runs {
		if !provider.KnownCompute(name) {
			return nil, fmt.Errorf(
				"%s names %q among the computes it runs, and ocel knows no such compute — it knows %s: upgrade ocel if %q is newer than this build, or pin a provider version this ocel understands",
				vendor, name, english.And(english.Quoted(provider.ComputeNames(provider.Computes()))), name,
			)
		}
		computes = append(computes, provider.Compute(name))
	}

	resolved := *p
	resolved.Apps = make([]App, 0, len(p.Apps))
	for _, app := range p.Apps {
		compute := app.Compute
		if compute == "" {
			compute = computes[0]
		}
		if !slices.Contains(computes, compute) {
			return nil, fmt.Errorf(
				"app %q asks for compute %q, which %s does not run — it runs %s: give %q a compute from that list, or deploy it to a provider that runs %q",
				app.Name, compute, vendor, english.And(english.Quoted(runs)), app.Name, compute,
			)
		}
		shaped, err := app.runningOn(compute)
		if err != nil {
			return nil, err
		}
		resolved.Apps = append(resolved.Apps, shaped)
	}
	return &resolved, nil
}

func (p *Project) ResolveDeclaredComputes() (*Project, error) {
	if names := p.UnresolvedApps(); len(names) > 0 {
		return nil, fmt.Errorf("%s %s no compute, and only the provider this project deploys through can say which one an app that names none runs on", english.And(english.Quoted(names)), nameOrNames(len(names)))
	}
	return p.ResolveComputes(provider.ComputeNames(provider.Computes()), "ocel")
}

func nameOrNames(n int) string {
	if n == 1 {
		return "names"
	}
	return "name"
}

func (p *Project) UnresolvedApps() []string {
	var names []string
	for _, app := range p.Apps {
		if app.Compute == "" {
			names = append(names, app.Name)
		}
	}
	return names
}

func (a App) runningOn(compute provider.Compute) (App, error) {
	a.Compute = compute
	if compute == provider.ComputeContainer {
		if a.Serverless != nil && !a.Serverless.Detected {
			return App{}, frameworkOnContainer(a.Name, a.Serverless.Framework, compute)
		}
		a.Serverless = nil
		if a.Container == nil {
			a.Container = &Container{}
		}
		return a, nil
	}
	if a.Serverless == nil {
		if a.undetected != nil {
			return App{}, a.undetected
		}
		return App{}, fmt.Errorf("app %q: nothing in %s says what it is built with; set \"framework\" in the app config", a.Name, a.Path)
	}
	if a.Container != nil {
		if err := refuseContainerConfig(a, compute, a.Container.Build, a.Container.Health); err != nil {
			return App{}, err
		}
	}
	a.Container = nil
	return a, nil
}
