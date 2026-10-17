package providerserver

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
)

func planRetirement(ctx context.Context, p provider.Provider, live provider.LiveApps, progress progress.Log) (provider.Plan, error) {
	hooks := p.Hooks().Retirement
	if hooks == nil {
		return provider.Plan{}, nil
	}
	return hooks.Plan(ctx, live, progress)
}

func retire(ctx context.Context, p provider.Provider, live provider.LiveApps, progress progress.Log) error {
	hooks := p.Hooks().Retirement
	if hooks == nil {
		return nil
	}
	return hooks.Retire(ctx, live, progress)
}

func (r *deployRun) liveApps() provider.LiveApps {
	apps := make([]string, 0, len(r.spec.Apps))
	for _, entry := range r.spec.Apps {
		apps = append(apps, entry.App)
	}
	return provider.LiveApps{Slug: r.spec.Slug, Tier: r.spec.Tier, Env: r.spec.Env, Apps: apps}
}

func noAppLive(slug string, tier environment.Tier, env string) provider.LiveApps {
	return provider.LiveApps{Slug: slug, Tier: tier, Env: env}
}

func unretiredWarning(promotionID string, err error) string {
	return fmt.Sprintf("Promotion %s serves, but retiring what the apps it no longer deploys left running failed, and the next deploy retries it: %v", promotionID, err)
}
