package deploy

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func (r *Stacks) Preflight(ctx context.Context, pre provider.DeployPreflight) error {
	cfg, err := r.resolve(ctx, Scope{Tier: pre.Deploy.Tier, Slug: pre.Deploy.Slug, Env: pre.Deploy.Env, Edge: pre.Edge})
	if err != nil {
		return err
	}
	if err := checkEdgeReaderBootstrapped(cfg, pre.Edge); err != nil {
		return err
	}
	sessions := newSessionScope(naming.Sanitize(pre.Deploy.Slug), pre.Deploy.Env, cfg.StateTableARN)
	return checkInlinePolicyBudget(pre.Apps, sessions)
}

func checkEdgeReaderBootstrapped(cfg Config, front edge.Kind) error {
	if cfg.Edge == nil || !cfg.Edge.Facts().RunsCode {
		return nil
	}
	switch {
	case cfg.EdgeCredentialsErr != nil:
		return fmt.Errorf("read the credential the %s edge reads the asset bucket with: %w", front, cfg.EdgeCredentialsErr)
	case !edgeReadsAssetBucket(cfg):
		return refusal.Refuse(refusal.CodeNotReady,
			"this bootstrap holds no credential for the %s edge to read the asset bucket with, so nothing this build caches could be served from it; re-run `%s`",
			front, provider.BootstrapCommand(cfg.Tier))
	case cfg.PurgeSeed == "":
		return refusal.Refuse(refusal.CodeNotReady,
			"this bootstrap holds no purge seed for the %s edge, so a revalidation could not purge what the edge has cached; re-run `%s`",
			front, provider.BootstrapCommand(cfg.Tier))
	}
	return nil
}
