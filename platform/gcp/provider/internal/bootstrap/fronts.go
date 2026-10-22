package bootstrap

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
	"github.com/ocelhq/ocel/pkg/stackrecords"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

const CloudflareOriginPart = "cloudflare-origin"

var ALBAPIs = []string{"compute.googleapis.com", "certificatemanager.googleapis.com", "networkservices.googleapis.com"}

func CredentialParts() []provider.Part {
	return []provider.Part{{
		Name: CloudflareOriginPart,
		Summary: "the origin secret the Cloudflare worker presents to every release it reaches, which the release demands before the app runs, " +
			"a service account with an HMAC key that reads the tier's static files and nothing else, which the worker signs its reads with, " +
			"and the credential and purge seed of the edge's workers, all in Secret Manager",
		Edges: []edge.Kind{cloudflare.Kind},
	}}
}

func InstalledParts(offered []provider.Part, recorded []string, req provider.BootstrapRequest) []string {
	var installed []string
	for _, part := range offered {
		named := slices.Contains(recorded, part.Name) || slices.Contains(req.Parts, part.Name)
		if named && !slices.Contains(req.Remove, part.Name) {
			installed = append(installed, part.Name)
		}
	}
	return installed
}

func (b Engine) fronted(parts []string) []provider.Part {
	var wanted []provider.Part
	for _, part := range CredentialParts() {
		if slices.Contains(parts, part.Name) && len(part.Edges) > 0 {
			wanted = append(wanted, part)
		}
	}
	return wanted
}

func (b Engine) eachFront(parts []string, visit func(provider.Part, edge.Edge) error) error {
	wanted := b.fronted(parts)
	if len(wanted) == 0 {
		return nil
	}
	if b.Edges == nil {
		return refusal.Refuse(refusal.CodeInvalid,
			"%s installs an edge's front, and this bootstrap was opened with no edge registry to open it through", wanted[0].Name)
	}
	for _, part := range wanted {
		for _, kind := range part.Edges {
			front, err := b.Edges.Open(kind, nil)
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

var frontedParts = []string{KeysPart, EdgeBucketPart}

func (b Engine) raiseParts(ctx context.Context, req provider.BootstrapRequest, progress progress.Log) error {
	if len(b.fronted(req.Parts)) > 0 {
		if err := b.ensureParts(ctx, req.Tier, "", frontedParts, !req.RefuseReplacements); err != nil {
			return err
		}
	}
	return b.RaiseFronts(ctx, req, progress)
}

func (b Engine) PlanFrontedParts(ctx context.Context, req provider.BootstrapRequest) ([]provider.ChangeGroup, error) {
	if len(b.fronted(req.Parts)) == 0 {
		return nil, nil
	}
	return b.PlanParts(ctx, req.Tier, frontedParts)
}

func (b Engine) summaryOf(name string) string {
	for _, part := range CredentialParts() {
		if part.Name == name {
			return part.Summary
		}
	}
	return ""
}

func (b Engine) RaiseFronts(ctx context.Context, req provider.BootstrapRequest, progress progress.Log) error {
	return b.eachFront(req.Parts, func(part provider.Part, front edge.Edge) error {
		gcloud.EnsureProgress(progress).Say("Adding the " + part.Name + " part of the " + string(req.Tier) + " bootstrap")
		out, err := front.Bootstrap(ctx, req.Tier)
		if err != nil {
			return err
		}
		if err := b.FrontParts.Adopt(ctx, b, req.Tier, front.Kind(), out, progress); err != nil {
			return err
		}
		if !front.Facts().RunsCode {
			return nil
		}
		gcloud.EnsureProgress(progress).Say("Keeping the " + string(front.Kind()) + " edge's asset reader for " + string(req.Tier))
		if err := b.FrontParts.RaiseAssets(ctx, b, req.Tier, front.Kind()); err != nil {
			return err
		}
		gcloud.EnsureProgress(progress).Say("Keeping the " + string(front.Kind()) + " edge's origin secret for " + string(req.Tier))
		return b.FrontParts.RaiseOriginSecret(ctx, b, req.Tier, front.Kind(), progress)
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

func (b Engine) DropFronts(
	ctx context.Context,
	read Survey,
	req provider.BootstrapRequest,
	progress progress.Log,
) error {
	dropping := droppedParts(read.Stamp.Parts, req)
	if err := b.FrontsFree(ctx, req.Tier, dropping); err != nil {
		return err
	}
	return b.eachFront(dropping, func(part provider.Part, front edge.Edge) error {
		gcloud.EnsureProgress(progress).Say("Taking down the " + string(front.Kind()) + " edge's front for " + string(req.Tier) +
			": the " + part.Name + " part is being removed")
		return b.retireFront(ctx, req.Tier, front)
	})
}

func (b Engine) dropParts(ctx context.Context, read Survey, req provider.BootstrapRequest, progress progress.Log) error {
	return b.DropFronts(ctx, read, req, progress)
}

func (b Engine) tearParts(ctx context.Context, tier environment.Tier, parts []string) error {
	return b.TearFronts(ctx, tier, parts)
}

func (b Engine) partInstalled(ctx context.Context, tier environment.Tier, part string) (bool, error) {
	return b.FrontInstalled(ctx, tier, part)
}

func (b Engine) TearFronts(ctx context.Context, tier environment.Tier, parts []string) error {
	return b.eachFront(parts, func(_ provider.Part, front edge.Edge) error {
		return b.retireFront(ctx, tier, front)
	})
}

func (b Engine) retireFront(ctx context.Context, tier environment.Tier, front edge.Edge) error {
	if err := front.Teardown(ctx, tier); err != nil {
		return err
	}
	if front.Facts().RunsCode {
		if err := b.FrontParts.TakeAssets(ctx, b, tier); err != nil {
			return err
		}
	}
	return b.FrontParts.Forget(ctx, b, tier, front.Kind())
}

func (b Engine) FrontInstalled(ctx context.Context, tier environment.Tier, part string) (bool, error) {
	installed := true
	err := b.eachFront([]string{part}, func(_ provider.Part, front edge.Edge) error {
		if checkInstalled := front.Hooks().CheckBootstrapInstalled; checkInstalled != nil {
			up, err := checkInstalled(ctx, tier)
			if err != nil {
				return err
			}
			installed = installed && up
		}
		if !installed || !front.Facts().RunsCode {
			return nil
		}
		held, err := b.FrontParts.HasOriginSecret(ctx, b, tier, front.Kind())
		if err != nil {
			return err
		}
		installed = held
		return nil
	})
	return installed, err
}

func (b Engine) FrontsFree(ctx context.Context, tier environment.Tier, parts []string) error {
	return b.eachFront(parts, func(part provider.Part, front edge.Edge) error {
		served, err := stackrecords.ProjectsServed(ctx, b.Records, tier, func(state stackrecords.EdgeState) bool {
			return state.Kind == front.Kind()
		})
		if err != nil {
			return err
		}
		if len(served) > 0 {
			return refusal.Refuse(refusal.CodeInvalid,
				"the %s edge of tier %s still serves %s, and removing the %s part takes away how it reaches them, leaving every one unreachable.\n"+
					"Take those projects down with `ocel projects destroy` first",
				front.Kind(), tier, strings.Join(served, ", "), part.Name)
		}
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

func (b Engine) partsFree(ctx context.Context, tier environment.Tier, parts []string) error {
	return b.FrontsFree(ctx, tier, parts)
}

func AppendMissing(listed, more []string) []string {
	for _, name := range more {
		if !slices.Contains(listed, name) {
			listed = append(listed, name)
		}
	}
	return listed
}

func (b Engine) PlannedFronts(ctx context.Context, tier environment.Tier, parts []string) ([]provider.ChangeGroup, error) {
	var groups []provider.ChangeGroup
	err := b.eachFront(parts, func(part provider.Part, front edge.Edge) error {
		var assets []provider.Change
		if front.Facts().RunsCode {
			var err error
			if assets, err = b.FrontParts.PlanAssets(ctx, b, tier, front.Kind()); err != nil {
				return fmt.Errorf("plan the %s edge's asset reader and origin secret: %w", front.Kind(), err)
			}
		}
		group, err := bootstrapplan.PlanEdgeBootstrap(ctx, front, tier, part.Name, assets...)
		if group != nil {
			groups = append(groups, *group)
		}
		return err
	})
	return groups, err
}

func (b Engine) RemovedFronts(ctx context.Context, tier environment.Tier, parts []string) ([]provider.ChangeGroup, error) {
	var groups []provider.ChangeGroup
	err := b.eachFront(parts, func(part provider.Part, front edge.Edge) error {
		var assets []provider.Change
		if front.Facts().RunsCode {
			var err error
			if assets, err = b.FrontParts.PlanAssetsRemoval(ctx, b, tier, front.Kind()); err != nil {
				return fmt.Errorf("plan what removing the %s edge's asset reader and origin secret takes: %w", front.Kind(), err)
			}
		}
		group, err := bootstrapplan.PlanEdgeRemoval(ctx, front, tier, part.Name, assets...)
		if group != nil {
			groups = append(groups, *group)
		}
		return err
	})
	return groups, err
}

type FrontParts struct {
	Adopt             func(ctx context.Context, b Engine, tier environment.Tier, kind edge.Kind, out edge.BootstrapOutput, progress progress.Log) error
	Forget            func(ctx context.Context, b Engine, tier environment.Tier, kind edge.Kind) error
	RaiseAssets       func(ctx context.Context, b Engine, tier environment.Tier, kind edge.Kind) error
	TakeAssets        func(ctx context.Context, b Engine, tier environment.Tier) error
	PlanAssets        func(ctx context.Context, b Engine, tier environment.Tier, kind edge.Kind) ([]provider.Change, error)
	PlanAssetsRemoval func(ctx context.Context, b Engine, tier environment.Tier, kind edge.Kind) ([]provider.Change, error)
	RaiseOriginSecret func(ctx context.Context, b Engine, tier environment.Tier, kind edge.Kind, progress progress.Log) error
	HasOriginSecret   func(ctx context.Context, b Engine, tier environment.Tier, kind edge.Kind) (bool, error)
}
