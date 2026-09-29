package box

import (
	"context"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

func (r Router) planProjectRemoval(scope edge.ProjectScope) []edge.PlanGroup {
	return r.e.ProjectRemovals(scope)
}

func (r Router) claimPreviewEntry(ctx context.Context, claim router.Claim) (edge.Origin, error) {
	base, wild := strings.CutPrefix(claim.Hostname, "*.")
	if !wild {
		return edge.Origin{}, refusal.Refuse(refusal.CodeInvalid, "a preview entry is a wildcard, and %q is none", claim.Hostname)
	}
	if err := host.PreviewBaseUsable(base); err != nil {
		return edge.Origin{}, err
	}
	address, err := r.e.machine.Address(ctx)
	if err != nil {
		return edge.Origin{}, err
	}
	certified, err := r.e.shield(ctx, claim, edge.PreviewEntryOwner)
	if err != nil {
		return edge.Origin{}, err
	}
	if err := r.e.machine.InstallPreviewEntry(ctx, base); err != nil {
		return edge.Origin{}, err
	}
	if len(claim.ClientCertificates) > 0 {
		if err := r.e.machine.RefuseUnshielded(ctx, claim.Hostname); err != nil {
			return edge.Origin{}, err
		}
	}
	return edge.Origin{Address: address, Certified: certified}, nil
}

func (r Router) disclaimPreviewEntry(ctx context.Context, baseDomain string) error {
	if err := r.e.machine.RemovePreviewEntry(ctx, baseDomain); err != nil {
		return err
	}
	return r.e.machine.UnshieldHost(ctx, edge.PreviewWildcard(baseDomain), edge.PreviewEntryOwner)
}

func (r Router) planPreviewEntryRemoval(wildcard string) []edge.PlanGroup {
	removed, _ := r.e.PreviewWildcardRemovals(wildcard)
	return []edge.PlanGroup{removed}
}
