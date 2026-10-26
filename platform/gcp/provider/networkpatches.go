package gcp

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/surface"
	"github.com/ocelhq/ocel/platform/gcp/provider/network"
)

func (p *Provider) evaluateNetworkPatches(ctx context.Context, tier environment.Tier) (transform.Result, error) {
	if evaluated, done := p.evaluatedNetworks.Load(tier); done {
		return evaluated.(transform.Result), nil
	}
	evaluated, err := p.evaluateTierNetwork(ctx, tier)
	if err != nil {
		return transform.Result{}, err
	}
	p.evaluatedNetworks.Store(tier, evaluated)
	return evaluated, nil
}

func (p *Provider) evaluateTierNetwork(ctx context.Context, tier environment.Tier) (transform.Result, error) {
	key := transform.Resource{Type: network.TransformKind, Name: string(tier)}
	req := surface.Request(tier, "", []transform.Resource{key})
	evaluated, err := surface.Evaluate(ctx, p.transforms, req, map[string]surface.Allowed{network.TransformKind: network.Allowed()})
	if err != nil {
		return transform.Result{}, err
	}
	result := evaluated.For(key.Type, key.Name)
	if err := transform.WalkOutputs(req, []transform.Result{result}, func(ref transform.OutputRef, at transform.OutputSite, _ any) (any, error) {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"a transform fills the %s tier's network's %s.%s from %s, and the network belongs to every project of the tier, so it reads no project's records",
			tier, at.Surface, at.Field, ref)
	}); err != nil {
		return transform.Result{}, err
	}
	return result, nil
}

func (p *Provider) withDeployNetworkPatches(ctx context.Context, b bootstrap.Engine, deploy provider.DeploySpec, parts []string) (bootstrap.Engine, error) {
	if !slices.Contains(parts, network.PartName) {
		return b, nil
	}
	evaluated, err := p.evaluateNetworkPatches(ctx, deploy.Tier)
	if err != nil {
		return bootstrap.Engine{}, err
	}
	return withNetworkPatches(ctx, b, deploy.Tier, deploy.Slug, evaluated)
}

func withNetworkPatches(ctx context.Context, b bootstrap.Engine, tier environment.Tier, project string, evaluated transform.Result) (bootstrap.Engine, error) {
	patches, err := network.ReadPatches(ctx, b, tier, project, evaluated)
	if err != nil {
		return bootstrap.Engine{}, err
	}
	b.Removable = tierParts(patches)
	return b, nil
}
