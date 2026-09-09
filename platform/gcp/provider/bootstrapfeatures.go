package gcp

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

const albFeature = "alb-edge"

var albAPIs = []string{"compute.googleapis.com", "certificatemanager.googleapis.com"}

func (bootstrapper) Catalogue() []providerkit.Feature {
	return []providerkit.Feature{{
		Name: albFeature,
		Summary: "a global external Application Load Balancer as the front: one address, one certificate map, one URL map — " +
			"the one bootstrap item with a standing cost, about $18 a month plus egress",
		Needs: []string{providerkit.NeedsEdgePrefix + string(alb.Kind)},
	}}
}

func (b bootstrapper) raiseFronts(ctx context.Context, req providerkit.BootstrapRequest, report providerkit.Reporter) error {
	if !slices.Contains(req.Features, albFeature) {
		return nil
	}
	if b.fronts == nil {
		return providerkit.Refuse(providerkit.CodeInvalid,
			"the %s feature stands the %q edge's load balancer up, and this bootstrap was opened with no edge registry to open it through",
			albFeature, alb.Kind)
	}
	front, err := b.fronts.Open(alb.Kind)
	if err != nil {
		return err
	}
	say(report, "standing the "+string(alb.Kind)+" load balancer up for "+string(req.Class)+": "+alb.StandingCost)
	_, err = front.Bootstrap(ctx, req.Class)
	return err
}

func (b bootstrapper) tearFronts(ctx context.Context, class providerkit.Class) error {
	if b.fronts == nil {
		return nil
	}
	front, err := b.fronts.Open(alb.Kind)
	if err != nil {
		return err
	}
	return front.Teardown(ctx, class)
}

func apisFor(features []string) []string {
	if !slices.Contains(features, albFeature) {
		return slices.Clone(BootstrapAPIs)
	}
	return slices.Concat(BootstrapAPIs, albAPIs)
}
