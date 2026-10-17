package fake

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
)

const AppGroupKind = "app"

func (p *Provider) PlanRetirement(_ context.Context, live provider.LiveApps, _ progress.Log) (provider.Plan, error) {
	var plan provider.Plan
	for _, app := range p.listRemovedApps(live) {
		plan.Groups = append(plan.Groups, provider.ChangeGroup{Kind: AppGroupKind, Name: app, Action: provider.ActionDelete, Reason: "no longer deployed to " + live.Env})
	}
	return plan, nil
}

func (p *Provider) Retire(_ context.Context, live provider.LiveApps, progress progress.Log) error {
	removed := p.listRemovedApps(live)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.retirements = append(p.retirements, live)
	for _, app := range removed {
		p.retired = append(p.retired, retiredApp{slug: live.Slug, env: live.Env, app: app, after: len(p.stacks.Provisioned())})
		if progress != nil {
			progress.Say("Retired app " + app)
		}
	}
	return nil
}

func (p *Provider) Retirements() []provider.LiveApps {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.retirements)
}

type retiredApp struct {
	slug, env, app string
	after          int
}

func (p *Provider) listRemovedApps(live provider.LiveApps) []string {
	provisioned := p.stacks.Provisioned()
	p.mu.Lock()
	defer p.mu.Unlock()
	var removed []string
	for at, spec := range provisioned {
		if spec.App == nil || spec.Ref.Project != live.Slug || spec.Ref.Tier != live.Tier || spec.Ref.Name.Env != live.Env {
			continue
		}
		app := spec.Ref.Name.App
		if live.Has(app) || slices.Contains(removed, app) || slices.ContainsFunc(p.retired, func(r retiredApp) bool {
			return r.slug == live.Slug && r.env == live.Env && r.app == app && r.after > at
		}) {
			continue
		}
		removed = append(removed, app)
	}
	return removed
}
