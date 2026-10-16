package gcp

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
	"github.com/ocelhq/ocel/pkg/refusal"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

const (
	albFeature         = "alb-edge"
	albShieldedFeature = "alb-cloudflare-origin"
)

var (
	albBaseAPIs     = []string{"compute.googleapis.com", "certificatemanager.googleapis.com"}
	albAPIs         = slices.Concat(albBaseAPIs, []string{"networkservices.googleapis.com"})
	albShieldedAPIs = slices.Concat(albBaseAPIs, []string{"networksecurity.googleapis.com"})
)

func (bootstrap) Catalogue() []provider.Feature {
	return []provider.Feature{{
		Name: albFeature,
		Summary: "a global external Application Load Balancer as the front: one address, one certificate map, one URL map — " +
			"the one bootstrap item with a recurring cost, about $18 a month plus egress, " +
			"a custom role that may only clear Cloud CDN, which Next apps behind it are granted, " +
			"and the gate as a Service Extensions plugin that runs in front of Cloud CDN on each tier, " +
			"kept as a generic artifact in a repository in Artifact Registry",
		Edges: []edge.Kind{alb.Kind},
	}, {
		Name: albShieldedFeature,
		Summary: "a global external Application Load Balancer that Cloudflare forwards to and that refuses any client without the certificate the zone presents — " +
			"a recurring cost, about $18 a month plus egress, " +
			"and a service account with an HMAC key that reads the tier's static files and nothing else, which the worker signs its reads with",
		Edges: []edge.Kind{cloudflare.Kind},
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
	return b.raiseCDNPurgeRole(ctx, req, progress)
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
		out, err := front.Bootstrap(ctx, req.Tier)
		if err != nil {
			return err
		}
		if err := adoptEdgeOffers(ctx, b.clients, b.records, req.Tier, front.Kind(), out, progress); err != nil {
			return err
		}
		if !front.Facts().RunsCode {
			return nil
		}
		ensureProgress(progress).Say("Keeping the " + string(front.Kind()) + " edge's asset reader (service account and HMAC key) for " + string(req.Tier) +
			": the edge reads this tier's static files from its bucket as that account")
		return b.clients.raiseAssetStore(ctx, req.Tier, front.Kind())
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
		return b.retireFront(ctx, req.Tier, front)
	})
}

func (b bootstrap) dropFeatures(ctx context.Context, read survey, req provider.BootstrapRequest, progress progress.Log) error {
	if err := b.dropFronts(ctx, read, req, progress); err != nil {
		return err
	}
	if slices.Contains(droppedFeatures(read.Stamp.Features, req), albFeature) {
		ensureProgress(progress).Say("Kept custom role " + read.Names.CDNPurgeRolePath() + ": " + reasonRoleKept)
	}
	return nil
}

func (b bootstrap) tearFeatures(ctx context.Context, tier environment.Tier, features []string) error {
	return b.tearFronts(ctx, tier, features)
}

func (b bootstrap) featureInstalled(ctx context.Context, tier environment.Tier, feature string) (bool, error) {
	return b.frontInstalled(ctx, tier, feature)
}

func (b bootstrap) tearFronts(ctx context.Context, tier environment.Tier, features []string) error {
	return b.eachFront(features, func(_ provider.Feature, front edge.Edge) error {
		return b.retireFront(ctx, tier, front)
	})
}

func (b bootstrap) retireFront(ctx context.Context, tier environment.Tier, front edge.Edge) error {
	if err := front.Teardown(ctx, tier); err != nil {
		return err
	}
	if front.Facts().RunsCode {
		if err := b.clients.takeAssetStore(ctx, tier); err != nil {
			return err
		}
	}
	return forgetEdgeOffers(ctx, b.clients, b.records, tier, front.Kind())
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
	return b.frontsFree(ctx, tier, features)
}

func apisFor(tier environment.Tier, features []string) []string {
	apis := slices.Clone(BootstrapAPIs)
	switch {
	case slices.Contains(features, albShieldedFeature):
		apis = appendMissing(apis, albShieldedAPIs)
	case slices.Contains(features, albFeature):
		apis = appendMissing(apis, albAPIs)
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

func (b bootstrap) plannedFronts(ctx context.Context, tier environment.Tier, features []string) ([]provider.ChangeGroup, error) {
	var groups []provider.ChangeGroup
	err := b.eachFront(features, func(feature provider.Feature, front edge.Edge) error {
		var planned []edge.PlanChange
		if plan := front.Hooks().PlanBootstrap; plan != nil {
			var err error
			if planned, err = plan(ctx, tier); err != nil {
				return fmt.Errorf("plan the %s edge bootstrap: %w", front.Kind(), err)
			}
		}
		var assets []provider.Change
		if front.Facts().RunsCode {
			var err error
			if assets, err = b.clients.plannedAssetStore(ctx, tier, front.Kind()); err != nil {
				return fmt.Errorf("plan the %s edge's asset reader: %w", front.Kind(), err)
			}
		}
		if len(planned) == 0 && len(assets) == 0 {
			return nil
		}
		group, err := bootstrapplan.EdgeGroup(front.Kind(), feature.Name, planned, assets...)
		groups = append(groups, group)
		return err
	})
	return groups, err
}

func (b bootstrap) removedFronts(ctx context.Context, tier environment.Tier, features []string) ([]provider.ChangeGroup, error) {
	var groups []provider.ChangeGroup
	err := b.eachFront(features, func(feature provider.Feature, front edge.Edge) error {
		var changes []provider.Change
		if plan := front.Hooks().PlanRemoveBootstrap; plan != nil {
			planned, err := plan(ctx, tier)
			if err != nil {
				return fmt.Errorf("plan what removing the %s edge bootstrap takes: %w", front.Kind(), err)
			}
			if changes, err = bootstrapplan.EdgeChanges(front.Kind(), planned); err != nil {
				return err
			}
		}
		if front.Facts().RunsCode {
			assets, err := b.clients.plannedAssetStoreRemoval(ctx, tier, front.Kind())
			if err != nil {
				return fmt.Errorf("plan what removing the %s edge's asset reader takes: %w", front.Kind(), err)
			}
			changes = append(changes, assets...)
		}
		if len(changes) == 0 {
			return nil
		}
		groups = append(groups, provider.ChangeGroup{
			Kind:    edge.EdgeGroupKind,
			Name:    edge.EdgeGroupName(front.Kind()),
			Feature: feature.Name,
			Action:  provider.ActionDelete,
			Changes: changes,
		})
		return nil
	})
	return groups, err
}
