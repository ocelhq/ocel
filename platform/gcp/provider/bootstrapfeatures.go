package gcp

import (
	"context"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

const albFeature = "alb-edge"

var albAPIs = []string{"compute.googleapis.com", "certificatemanager.googleapis.com"}

func (bootstrap) Catalogue() []provider.Feature {
	return []provider.Feature{{
		Name: albFeature,
		Summary: "a global external Application Load Balancer as the front: one address, one certificate map, one URL map — " +
			"the one bootstrap item with a recurring cost, about $18 a month plus egress",
		Needs: []string{provider.NeedsEdgePrefix + string(alb.Kind)},
	}}
}

func installedFeatures(catalogue []provider.Feature, recorded []string, req provider.BootstrapRequest) []string {
	var installed []string
	for _, feature := range catalogue {
		named := slices.Contains(recorded, feature.Name) || slices.Contains(req.Features, feature.Name)
		if named && !slices.Contains(req.Remove, feature.Name) {
			installed = append(installed, feature.Name)
		}
	}
	return installed
}

func (b bootstrap) fronted(features []string) []provider.Feature {
	var wanted []provider.Feature
	for _, feature := range b.Catalogue() {
		if slices.Contains(features, feature.Name) && len(edgesNeededBy(feature)) > 0 {
			wanted = append(wanted, feature)
		}
	}
	return wanted
}

func edgesNeededBy(feature provider.Feature) []edge.Kind {
	var kinds []edge.Kind
	for _, need := range feature.Needs {
		if kind, named := strings.CutPrefix(need, provider.NeedsEdgePrefix); named {
			kinds = append(kinds, edge.Kind(kind))
		}
	}
	return kinds
}

func (b bootstrap) eachFront(features []string, visit func(provider.Feature, edge.Edge) error) error {
	wanted := b.fronted(features)
	if len(wanted) == 0 {
		return nil
	}
	if b.fronts == nil {
		return refusal.Refuse(refusal.CodeInvalid,
			"%s installs an edge's front, and this bootstrap was opened with no edge registry to open it through", wanted[0].Name)
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

func (b bootstrap) raiseFronts(ctx context.Context, req provider.BootstrapRequest, progress edge.Progress) error {
	return b.eachFront(req.Features, func(feature provider.Feature, front edge.Edge) error {
		say(progress, "installing the front of the "+string(front.Kind())+" edge for "+string(req.Class)+": "+feature.Summary)
		_, err := front.Bootstrap(ctx, req.Class)
		return err
	})
}

func droppedFeatures(recorded []string, req provider.BootstrapRequest) []string {
	var dropping []string
	for _, name := range req.Remove {
		if slices.Contains(recorded, name) && !slices.Contains(req.Features, name) {
			dropping = append(dropping, name)
		}
	}
	return dropping
}

func (b bootstrap) dropFronts(
	ctx context.Context,
	read survey,
	req provider.BootstrapRequest,
	progress edge.Progress,
) error {
	dropping := droppedFeatures(read.Stamp.Features, req)
	if err := b.frontsFree(ctx, req.Class, dropping); err != nil {
		return err
	}
	return b.eachFront(dropping, func(feature provider.Feature, front edge.Edge) error {
		say(progress, "taking the front of the "+string(front.Kind())+" edge down for "+string(req.Class)+": "+feature.Name+" was removed")
		return front.Teardown(ctx, req.Class)
	})
}

func (b bootstrap) tearFronts(ctx context.Context, class edge.Class, features []string) error {
	return b.eachFront(features, func(_ provider.Feature, front edge.Edge) error {
		return front.Teardown(ctx, class)
	})
}

func (b bootstrap) frontInstalled(ctx context.Context, class edge.Class, feature string) (bool, error) {
	installed := true
	err := b.eachFront([]string{feature}, func(_ provider.Feature, front edge.Edge) error {
		checkInstalled := front.Hooks().CheckBootstrapInstalled
		if checkInstalled == nil {
			return nil
		}
		up, err := checkInstalled(ctx, class)
		if err != nil {
			return err
		}
		installed = installed && up
		return nil
	})
	return installed, err
}

func (b bootstrap) frontsFree(ctx context.Context, class edge.Class, features []string) error {
	return b.eachFront(features, func(_ provider.Feature, front edge.Edge) error {
		boundHostnames := front.Hooks().ListBoundHostnames
		if boundHostnames == nil {
			return nil
		}
		bound, err := boundHostnames(ctx, class)
		if err != nil || len(bound) == 0 {
			return err
		}
		return refusal.Refuse(refusal.CodeInvalid,
			"%s is still served by the %s front of class %s, and the front owns the certificate map those hostnames are entries in, "+
				"which Google will not delete while it contains any.\nRelease them with `ocel domain remove` in the projects that bound them, then remove this bootstrap",
			strings.Join(bound, ", "), front.Kind(), class)
	})
}

func apisFor(features []string) []string {
	if !slices.Contains(features, albFeature) {
		return slices.Clone(BootstrapAPIs)
	}
	return slices.Concat(BootstrapAPIs, albAPIs)
}
