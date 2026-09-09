package gcp

import (
	"context"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
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

func standingFeatures(catalogue []providerkit.Feature, held []string, req providerkit.BootstrapRequest) []string {
	var standing []string
	for _, feature := range catalogue {
		named := slices.Contains(held, feature.Name) || slices.Contains(req.Features, feature.Name)
		if named && !slices.Contains(req.Remove, feature.Name) {
			standing = append(standing, feature.Name)
		}
	}
	return standing
}

func (b bootstrapper) fronted(features []string) []providerkit.Feature {
	var wanted []providerkit.Feature
	for _, feature := range b.Catalogue() {
		if slices.Contains(features, feature.Name) && len(edgesNeededBy(feature)) > 0 {
			wanted = append(wanted, feature)
		}
	}
	return wanted
}

func edgesNeededBy(feature providerkit.Feature) []edge.Kind {
	var kinds []edge.Kind
	for _, need := range feature.Needs {
		if kind, named := strings.CutPrefix(need, providerkit.NeedsEdgePrefix); named {
			kinds = append(kinds, edge.Kind(kind))
		}
	}
	return kinds
}

func (b bootstrapper) eachFront(features []string, visit func(providerkit.Feature, edge.Edge) error) error {
	wanted := b.fronted(features)
	if len(wanted) == 0 {
		return nil
	}
	if b.fronts == nil {
		return providerkit.Refuse(providerkit.CodeInvalid,
			"%s stands an edge's front up, and this bootstrap was opened with no edge registry to open it through", wanted[0].Name)
	}
	for _, feature := range wanted {
		for _, kind := range edgesNeededBy(feature) {
			front, err := b.fronts.Open(kind)
			if err != nil {
				return err
			}
			if err := visit(feature, front); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b bootstrapper) raiseFronts(ctx context.Context, req providerkit.BootstrapRequest, report providerkit.Reporter) error {
	return b.eachFront(req.Features, func(feature providerkit.Feature, front edge.Edge) error {
		say(report, "standing the front of the "+string(front.Kind())+" edge up for "+string(req.Class)+": "+feature.Summary)
		_, err := front.Bootstrap(ctx, req.Class)
		return err
	})
}

func (b bootstrapper) tearFronts(ctx context.Context, class providerkit.Class, features []string) error {
	return b.eachFront(features, func(_ providerkit.Feature, front edge.Edge) error {
		return front.Teardown(ctx, class)
	})
}

func (b bootstrapper) frontsFree(ctx context.Context, class providerkit.Class, features []string) error {
	return b.eachFront(features, func(_ providerkit.Feature, front edge.Edge) error {
		holder, holds := front.(interface {
			Bound(ctx context.Context, class edge.Class) ([]string, error)
		})
		if !holds {
			return nil
		}
		bound, err := holder.Bound(ctx, class)
		if err != nil || len(bound) == 0 {
			return err
		}
		return providerkit.Refuse(providerkit.CodeInvalid,
			"%s is still served by the %s front of class %s, and the front owns the certificate map those hostnames are entries in, "+
				"which Google will not delete while it holds any.\nRelease them with `ocel domain remove` in the projects that bound them, then remove this bootstrap",
			strings.Join(bound, ", "), front.Kind(), class)
	})
}

func apisFor(features []string) []string {
	if !slices.Contains(features, albFeature) {
		return slices.Clone(BootstrapAPIs)
	}
	return slices.Concat(BootstrapAPIs, albAPIs)
}
