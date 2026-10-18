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
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

const cloudflareOriginPart = "cloudflare-origin"

var (
	albBaseAPIs     = []string{"compute.googleapis.com", "certificatemanager.googleapis.com"}
	albAPIs         = slices.Concat(albBaseAPIs, []string{"networkservices.googleapis.com"})
	albShieldedAPIs = slices.Concat(albBaseAPIs, []string{"networksecurity.googleapis.com"})
)

func credentialParts() []provider.Part {
	return []provider.Part{{
		Name: cloudflareOriginPart,
		Summary: "a global external Application Load Balancer that Cloudflare forwards to and that refuses any client without the certificate the zone presents — " +
			"a recurring cost, about $18 a month plus egress, " +
			"and a service account with an HMAC key that reads the tier's static files and nothing else, which the worker signs its reads with",
		Edges: []edge.Kind{cloudflare.Kind},
	}}
}

func installedParts(offered []provider.Part, recorded []string, req provider.BootstrapRequest) []string {
	var installed []string
	for _, part := range offered {
		named := slices.Contains(recorded, part.Name) || slices.Contains(req.Parts, part.Name)
		if named && !slices.Contains(req.Remove, part.Name) {
			installed = append(installed, part.Name)
		}
	}
	return installed
}

func (b bootstrap) fronted(parts []string) []provider.Part {
	var wanted []provider.Part
	for _, part := range credentialParts() {
		if slices.Contains(parts, part.Name) && len(part.Edges) > 0 {
			wanted = append(wanted, part)
		}
	}
	return wanted
}

func (b bootstrap) eachFront(parts []string, visit func(provider.Part, edge.Edge) error) error {
	wanted := b.fronted(parts)
	if len(wanted) == 0 {
		return nil
	}
	if b.fronts == nil {
		return refusal.Refuse(refusal.CodeInvalid,
			"%s installs an edge's front, and this bootstrap was opened with no edge registry to open it through", wanted[0].Name)
	}
	for _, part := range wanted {
		for _, kind := range part.Edges {
			front, err := b.fronts.Open(kind, nil)
			if err != nil {
				return err
			}
			if err := visit(part, front); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b bootstrap) raiseParts(ctx context.Context, req provider.BootstrapRequest, progress progress.Log) error {
	return b.raiseFronts(ctx, req, progress)
}

func (b bootstrap) summaryOf(name string) string {
	for _, part := range credentialParts() {
		if part.Name == name {
			return part.Summary
		}
	}
	return ""
}

func (b bootstrap) raiseFronts(ctx context.Context, req provider.BootstrapRequest, progress progress.Log) error {
	return b.eachFront(req.Parts, func(part provider.Part, front edge.Edge) error {
		gcloud.EnsureProgress(progress).Say("Adding the " + part.Name + " part of the " + string(req.Tier) + " bootstrap: " + part.Summary)
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
		gcloud.EnsureProgress(progress).Say("Keeping the " + string(front.Kind()) + " edge's asset reader (service account and HMAC key) for " + string(req.Tier) +
			": the edge reads this tier's static files from its bucket as that account")
		return raiseAssetStore(ctx, b.clients, req.Tier, front.Kind())
	})
}

func droppedParts(recorded []string, req provider.BootstrapRequest) []string {
	var dropping []string
	for _, name := range req.Remove {
		if slices.Contains(recorded, name) && !slices.Contains(req.Parts, name) {
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
	dropping := droppedParts(read.Stamp.Parts, req)
	if err := b.frontsFree(ctx, req.Tier, dropping); err != nil {
		return err
	}
	return b.eachFront(dropping, func(part provider.Part, front edge.Edge) error {
		gcloud.EnsureProgress(progress).Say("Taking down the " + string(front.Kind()) + " edge's front for " + string(req.Tier) +
			": the " + part.Name + " part is being removed")
		return b.retireFront(ctx, req.Tier, front)
	})
}

func (b bootstrap) dropParts(ctx context.Context, read survey, req provider.BootstrapRequest, progress progress.Log) error {
	return b.dropFronts(ctx, read, req, progress)
}

func (b bootstrap) tearParts(ctx context.Context, tier environment.Tier, parts []string) error {
	return b.tearFronts(ctx, tier, parts)
}

func (b bootstrap) partInstalled(ctx context.Context, tier environment.Tier, part string) (bool, error) {
	return b.frontInstalled(ctx, tier, part)
}

func (b bootstrap) tearFronts(ctx context.Context, tier environment.Tier, parts []string) error {
	return b.eachFront(parts, func(_ provider.Part, front edge.Edge) error {
		return b.retireFront(ctx, tier, front)
	})
}

func (b bootstrap) retireFront(ctx context.Context, tier environment.Tier, front edge.Edge) error {
	if err := front.Teardown(ctx, tier); err != nil {
		return err
	}
	if front.Facts().RunsCode {
		if err := takeAssetStore(ctx, b.clients, tier); err != nil {
			return err
		}
	}
	return forgetEdgeOffers(ctx, b.clients, b.records, tier, front.Kind())
}

func (b bootstrap) frontInstalled(ctx context.Context, tier environment.Tier, part string) (bool, error) {
	installed := true
	err := b.eachFront([]string{part}, func(_ provider.Part, front edge.Edge) error {
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

func (b bootstrap) frontsFree(ctx context.Context, tier environment.Tier, parts []string) error {
	return b.eachFront(parts, func(_ provider.Part, front edge.Edge) error {
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

func (b bootstrap) partsFree(ctx context.Context, tier environment.Tier, parts []string) error {
	return b.frontsFree(ctx, tier, parts)
}

func apisFor(tier environment.Tier, parts []string) []string {
	apis := slices.Clone(BootstrapAPIs)
	if slices.Contains(parts, cloudflareOriginPart) {
		apis = appendMissing(apis, albShieldedAPIs)
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

func (b bootstrap) plannedFronts(ctx context.Context, tier environment.Tier, parts []string) ([]provider.ChangeGroup, error) {
	var groups []provider.ChangeGroup
	err := b.eachFront(parts, func(part provider.Part, front edge.Edge) error {
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
			if assets, err = plannedAssetStore(ctx, b.clients, tier, front.Kind()); err != nil {
				return fmt.Errorf("plan the %s edge's asset reader: %w", front.Kind(), err)
			}
		}
		if len(planned) == 0 && len(assets) == 0 {
			return nil
		}
		group, err := bootstrapplan.EdgeGroup(front.Kind(), part.Name, planned, assets...)
		groups = append(groups, group)
		return err
	})
	return groups, err
}

func (b bootstrap) removedFronts(ctx context.Context, tier environment.Tier, parts []string) ([]provider.ChangeGroup, error) {
	var groups []provider.ChangeGroup
	err := b.eachFront(parts, func(part provider.Part, front edge.Edge) error {
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
			assets, err := plannedAssetStoreRemoval(ctx, b.clients, tier, front.Kind())
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
			Part:    part.Name,
			Action:  provider.ActionDelete,
			Changes: changes,
		})
		return nil
	})
	return groups, err
}
