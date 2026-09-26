package deploy

import (
	"context"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
)

func (r *Stacks) Preflight(ctx context.Context, pre provider.DeployPreflight) error {
	cfg, err := r.resolve(ctx, Scope{Class: pre.Plan.Class, Slug: pre.Plan.Slug, Env: pre.Plan.Env, Edge: pre.Edge})
	if err != nil {
		return err
	}
	if err := checkISRWriterAgrees(cfg.Class, cfg.objectStores(), cfg.isrWriter()); err != nil {
		return err
	}
	sessions := newSessionScope(naming.Sanitize(pre.Plan.Slug), pre.Plan.Env, cfg.StateTableARN)
	return checkInlinePolicyBudget(pre.Apps, sessions)
}
