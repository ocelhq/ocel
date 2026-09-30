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
	if claim.Tunnel != edge.None {
		origin, err := r.e.tunnelHost(ctx, claim.Hostname, claim.Tunnel, edge.PreviewEntryOwner)
		if err != nil {
			return edge.Origin{}, err
		}
		return origin, r.e.machine.InstallPreviewEntry(ctx, base)
	}
	address, err := r.e.machine.Address(ctx)
	if err != nil {
		return edge.Origin{}, err
	}
	certified := true
	shielded := len(claim.ClientCertificates) > 0
	if shielded {
		if certified, err = r.e.putShield(ctx, claim, edge.PreviewEntryOwner); err != nil {
			return edge.Origin{}, err
		}
	}
	if err := r.e.machine.InstallPreviewEntry(ctx, base); err != nil {
		return edge.Origin{}, err
	}
	if err := r.e.removeTunneledHost(ctx, claim.Hostname, edge.PreviewEntryOwner); err != nil {
		return edge.Origin{}, err
	}
	if shielded {
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
	if err := r.e.machine.RemoveShield(ctx, edge.PreviewWildcard(baseDomain), edge.PreviewEntryOwner); err != nil {
		return err
	}
	return r.e.removeTunneledHost(ctx, edge.PreviewWildcard(baseDomain), edge.PreviewEntryOwner)
}

func (r Router) planPreviewEntryRemoval(wildcard string) []edge.PlanGroup {
	removed, _ := r.e.PreviewWildcardRemovals(wildcard)
	return []edge.PlanGroup{removed}
}
