package alb

import (
	"context"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
)

func (r Router) planProjectRemoval(scope edge.ProjectScope) []edge.PlanGroup {
	return r.e.ProjectRemovals(scope)
}

func (r Router) claimPreviewEntry(ctx context.Context, claim router.Claim) (edge.Origin, error) {
	base, wild := strings.CutPrefix(claim.Hostname, "*.")
	if !wild {
		return edge.Origin{}, refusal.Refuse(refusal.CodeInvalid, "a preview entry is a wildcard, and %q is none", claim.Hostname)
	}
	balancing := r.e.loadBalancerFor(claim)
	if len(claim.ClientCertificates) > 0 {
		if _, err := balancing.trustClaim(ctx, environment.TierPreview, claim.Hostname, claim.ClientCertificates); err != nil {
			return edge.Origin{}, err
		}
	}
	address, err := balancing.ReconcilePreviewWildcard(ctx, edge.PreviewWildcardSpec{BaseDomain: base, Certificate: claim.Certificate})
	if err != nil {
		return edge.Origin{}, err
	}
	return edge.Origin{Address: address, Certified: true}, nil
}

func (r Router) disclaimPreviewEntry(ctx context.Context, baseDomain string) error {
	recorded, err := r.e.recordedPreview(ctx)
	if err != nil || recorded.BaseDomain != baseDomain {
		return err
	}
	if !recorded.Shielded {
		return r.e.DestroyPreviewWildcard(ctx, baseDomain)
	}
	shielded := r.e.Shielded()
	if err := shielded.DestroyPreviewWildcard(ctx, baseDomain); err != nil {
		return err
	}
	return shielded.withdrawClaims(ctx, environment.TierPreview, edge.PreviewWildcard(baseDomain))
}

func (r Router) planPreviewEntryRemoval(wildcard string) []edge.PlanGroup {
	removed, _ := r.e.PreviewWildcardRemovals(wildcard)
	return []edge.PlanGroup{removed}
}
