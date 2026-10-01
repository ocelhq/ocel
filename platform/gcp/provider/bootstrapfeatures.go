package gcp

import (
	"context"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

const (
	albFeature         = "alb-edge"
	albShieldedFeature = "alb-cloudflare-origin"
	kvFeature          = "kv-network"
)

var (
	albAPIs         = []string{"compute.googleapis.com", "certificatemanager.googleapis.com"}
	albShieldedAPIs = slices.Concat(albAPIs, []string{"networksecurity.googleapis.com"})
)

func (bootstrap) Catalogue() []provider.Feature {
	return []provider.Feature{{
		Name: albFeature,
		Summary: "a global external Application Load Balancer as the front: one address, one certificate map, one URL map — " +
			"the one bootstrap item with a recurring cost, about $18 a month plus egress",
		Edges: []edge.Kind{alb.Kind},
	}, {
		Name: albShieldedFeature,
		Summary: "a global external Application Load Balancer that Cloudflare forwards to and that refuses any client without the certificate the zone presents — " +
			"a recurring cost, about $18 a month plus egress",
		Edges: []edge.Kind{cloudflare.Kind},
	}, {
		Name: kvFeature,
		Summary: "a network of the tier's own with one subnetwork, " + kvSubnetRange + ", and the service connection policy Memorystore reaches it through: " +
			"what kv stores and the apps bound to them are connected over, with no recurring cost of its own",
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
		if slices.Contains(features, feature.Name) && len(feature.Edges) > 0 {
			wanted = append(wanted, feature)
		}
	}
	return wanted
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
		for _, kind := range feature.Edges {
			front, err := b.fronts.Open(kind, nil)
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

func (b bootstrap) raiseFeatures(ctx context.Context, req provider.BootstrapRequest, progress progress.Log) error {
	if err := b.raiseFronts(ctx, req, progress); err != nil {
		return err
	}
	if !slices.Contains(req.Features, kvFeature) {
		return nil
	}
	ensureProgress(progress).Say("Installing feature " + kvFeature + " for " + string(req.Tier) + ": " + b.summaryOf(kvFeature))
	return b.raiseNetwork(ctx, req.Tier, progress)
}

func (b bootstrap) summaryOf(name string) string {
	for _, feature := range b.Catalogue() {
		if feature.Name == name {
			return feature.Summary
		}
	}
	return ""
}

func (b bootstrap) raiseFronts(ctx context.Context, req provider.BootstrapRequest, progress progress.Log) error {
	return b.eachFront(req.Features, func(feature provider.Feature, front edge.Edge) error {
		ensureProgress(progress).Say("Installing feature " + feature.Name + " for " + string(req.Tier) + ": " + feature.Summary)
		_, err := front.Bootstrap(ctx, req.Tier)
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
	progress progress.Log,
) error {
	dropping := droppedFeatures(read.Stamp.Features, req)
	if err := b.frontsFree(ctx, req.Tier, dropping); err != nil {
		return err
	}
	return b.eachFront(dropping, func(feature provider.Feature, front edge.Edge) error {
		ensureProgress(progress).Say("Taking down the " + string(front.Kind()) + " edge's front for " + string(req.Tier) +
			": this bootstrap no longer requests feature " + feature.Name)
		return front.Teardown(ctx, req.Tier)
	})
}

func (b bootstrap) dropFeatures(ctx context.Context, read survey, req provider.BootstrapRequest, progress progress.Log) error {
	if err := b.networkFree(ctx, req.Tier, droppedFeatures(read.Stamp.Features, req)); err != nil {
		return err
	}
	if err := b.dropFronts(ctx, read, req, progress); err != nil {
		return err
	}
	if !slices.Contains(droppedFeatures(read.Stamp.Features, req), kvFeature) {
		return nil
	}
	ensureProgress(progress).Say("Taking down the " + string(req.Tier) + " kv network: this bootstrap no longer requests feature " + kvFeature)
	return b.tearNetwork(ctx, req.Tier)
}

func (b bootstrap) tearFeatures(ctx context.Context, tier environment.Tier, features []string) error {
	if err := b.tearFronts(ctx, tier, features); err != nil {
		return err
	}
	if !slices.Contains(features, kvFeature) {
		return nil
	}
	return b.tearNetwork(ctx, tier)
}

func (b bootstrap) featureInstalled(ctx context.Context, tier environment.Tier, feature string) (bool, error) {
	if feature == kvFeature {
		return b.networkInstalled(ctx, tier)
	}
	return b.frontInstalled(ctx, tier, feature)
}

func (b bootstrap) tearFronts(ctx context.Context, tier environment.Tier, features []string) error {
	return b.eachFront(features, func(_ provider.Feature, front edge.Edge) error {
		return front.Teardown(ctx, tier)
	})
}

func (b bootstrap) frontInstalled(ctx context.Context, tier environment.Tier, feature string) (bool, error) {
	installed := true
	err := b.eachFront([]string{feature}, func(_ provider.Feature, front edge.Edge) error {
		checkInstalled := front.Hooks().CheckBootstrapInstalled
		if checkInstalled == nil {
			return nil
		}
		up, err := checkInstalled(ctx, tier)
		if err != nil {
			return err
		}
		installed = installed && up
		return nil
	})
	return installed, err
}

func (b bootstrap) frontsFree(ctx context.Context, tier environment.Tier, features []string) error {
	return b.eachFront(features, func(_ provider.Feature, front edge.Edge) error {
		boundHostnames := front.Hooks().ListBoundHostnames
		if boundHostnames == nil {
			return nil
		}
		bound, err := boundHostnames(ctx, tier)
		if err != nil || len(bound) == 0 {
			return err
		}
		return refusal.Refuse(refusal.CodeInvalid,
			"%s is still served by the %s front of tier %s, and the front owns the certificate map those hostnames are entries in, "+
				"which Google will not delete while it contains any.\nRelease them with `ocel domain remove` in the projects that bound them, then remove this bootstrap",
			strings.Join(bound, ", "), front.Kind(), tier)
	})
}

func (b bootstrap) featuresFree(ctx context.Context, tier environment.Tier, features []string) error {
	if err := b.networkFree(ctx, tier, features); err != nil {
		return err
	}
	return b.frontsFree(ctx, tier, features)
}

func apisFor(features []string) []string {
	apis := slices.Clone(BootstrapAPIs)
	switch {
	case slices.Contains(features, albShieldedFeature):
		apis = appendMissing(apis, albShieldedAPIs)
	case slices.Contains(features, albFeature):
		apis = appendMissing(apis, albAPIs)
	}
	if slices.Contains(features, kvFeature) {
		apis = appendMissing(apis, kvAPIs)
	}
	return apis
}

func appendMissing(listed, more []string) []string {
	for _, name := range more {
		if !slices.Contains(listed, name) {
			listed = append(listed, name)
		}
	}
	return listed
}
