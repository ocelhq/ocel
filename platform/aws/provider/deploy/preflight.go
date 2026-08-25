package deploy

import (
	"context"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/membrane"
)

func (r *Releaser) Preflight(ctx context.Context, pre providerkit.DeployPreflight) error {
	if err := checkMembraneServices(pre.Resources, pre.Grants, membrane.Serves); err != nil {
		return err
	}
	cfg, err := r.resolve.Release(ctx, Scope{Class: pre.Plan.Class, Slug: pre.Plan.Slug, Env: pre.Plan.Env, Edge: pre.Edge})
	if err != nil {
		return err
	}
	if err := checkISRWriterAgrees(cfg.Class, cfg.objectStores(), cfg.isrWriter()); err != nil {
		return err
	}
	sessions := newSessionScope(naming.Sanitize(pre.Plan.Slug), pre.Plan.Env, cfg.StateTableARN)
	return checkInlinePolicyBudget(pre.Apps, sessions)
}
