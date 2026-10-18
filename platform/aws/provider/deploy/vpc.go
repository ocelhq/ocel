package deploy

import (
	"context"
	"strings"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/vpcclients"
)

func (r *release) placeInVPC(ctx context.Context, spec provider.StackSpec, work *infraWork) error {
	placed := r.registry.PlacesInVPC(spec.Resources)
	if len(placed) == 0 {
		return nil
	}
	names := make([]string, 0, len(placed))
	for _, resource := range placed {
		names = append(names, string(resource.Type)+" "+resource.Declared)
	}
	vpc, err := component.ReadDefaultVPC(ctx, r.cfg.Network, r.cfg.Region, strings.Join(names, ", "))
	if err != nil {
		return err
	}
	work.vpc = &vpc
	work.clients = r.cfg.VPCClients
	switch {
	case work.clients != "":
	case work.previewing:
		work.clients = component.PendingClients
	default:
		return refusal.Refuse(refusal.CodeNotReady,
			"%s admit the %s tier's %s security group alone, and it is not recorded: deploy again, and its preflight adds it",
			strings.Join(names, ", "), spec.Ref.Tier, vpcclients.Part)
	}
	return nil
}
