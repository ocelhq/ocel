package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

func (p *Provider) readEdgePurgeSecret(ctx context.Context, spec provider.StackSpec) (string, error) {
	if !signsEdgePurges(spec) {
		return "", nil
	}
	c, err := p.openClients(ctx)
	if err != nil {
		return "", err
	}
	if _, _, err := requireAdoptedEdge(ctx, c, p.KeyValues(), spec.Ref.Tier, spec.Edge.Kind()); err != nil {
		return "", err
	}
	seed, err := readPurgeSeed(ctx, c, spec.Ref.Tier, spec.Edge.Kind())
	if err != nil {
		return "", err
	}
	return cloudflare.DerivePurgeSecret(seed, spec.App.ISR.Prefix), nil
}

type stacks struct {
	provider.Stacks
	p *Provider
}

func (s stacks) Plan(ctx context.Context, spec provider.StackSpec, runProgress progress.Log) (provider.Plan, error) {
	spec, err := s.p.withFunctionImages(ctx, spec)
	if err != nil {
		return provider.Plan{}, err
	}
	return s.Stacks.Plan(ctx, spec, runProgress)
}

func (s stacks) Provision(ctx context.Context, spec provider.StackSpec, runProgress progress.Log) (provider.StackResult, error) {
	facts, err := s.p.readEdgeFacts(ctx, spec)
	if err != nil {
		return provider.StackResult{}, err
	}
	if spec, err = s.p.withFunctionImages(ctx, spec); err != nil {
		return provider.StackResult{}, err
	}
	ctx = withEdgeFacts(ctx, spec, facts)
	routeTableKey, err := s.p.putEdgeRouteTable(ctx, spec, runProgress)
	if err != nil {
		return provider.StackResult{}, err
	}
	result, err := s.Stacks.Provision(ctx, spec, runProgress)
	if err != nil {
		return result, err
	}
	result.RouteTableKey = routeTableKey
	result.PurgeSecret = facts.purgeSecret
	return result, nil
}
