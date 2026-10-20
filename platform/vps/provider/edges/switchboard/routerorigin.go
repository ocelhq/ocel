package switchboard

import (
	"context"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/routing"
)

func (r Router) planProjectRemoval(scope edge.ProjectScope) ([]edge.PlanGroup, error) {
	return r.e.PlanProjectRemoval(scope)
}

func (r Router) claimPreviewEntry(ctx context.Context, claim router.Claim) (edge.Origin, error) {
	base, wild := strings.CutPrefix(claim.Hostname, "*.")
	if !wild {
		return edge.Origin{}, refusal.Refuse(refusal.CodeInvalid, "a preview entry is a wildcard, and %q is none", claim.Hostname)
	}
	if err := routing.RefuseInvalidPreviewBase(base); err != nil {
		return edge.Origin{}, err
	}
	if claim.Tunnel != edge.None {
		return r.e.claimThrough(ctx, claim.Tunnel, edge.PreviewEntryOwner, claim.Hostname, func(tunnel boxapi.Tunnel) error {
			return r.e.routes.InstallPreviewEntryThrough(ctx, base, tunnel.Name)
		})
	}
	address, err := r.e.machine.Address(ctx)
	if err != nil {
		return edge.Origin{}, err
	}
	certified := true
	shielded := len(claim.ClientCAs) > 0
	if shielded {
		if certified, err = r.e.putShield(ctx, claim, edge.PreviewEntryOwner); err != nil {
			return edge.Origin{}, err
		}
	}
	if err := r.e.routes.InstallPreviewEntry(ctx, base); err != nil {
		return edge.Origin{}, err
	}
	if err := r.e.releaseTunnel(ctx); err != nil {
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
	if err := r.e.routes.RemovePreviewEntry(ctx, baseDomain); err != nil {
		return err
	}
	if err := r.e.routes.RemoveShield(ctx, edge.PreviewWildcard(baseDomain), edge.PreviewEntryOwner); err != nil {
		return err
	}
	return r.e.releaseTunnel(ctx)
}

func (r Router) planPreviewEntryRemoval(wildcard string) ([]edge.PlanGroup, error) {
	removed, _, err := r.e.PlanPreviewWildcardRemoval(wildcard)
	if err != nil {
		return nil, err
	}
	return []edge.PlanGroup{removed}, nil
}
