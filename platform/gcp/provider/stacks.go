package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/compute/release"
)

type stacks struct {
	provider.Stacks
	p *Provider
}

func (s stacks) Plan(ctx context.Context, spec provider.StackSpec, runProgress progress.Log) (provider.Plan, error) {
	spec, err := s.p.functionImages().WithImages(ctx, spec)
	if err != nil {
		return provider.Plan{}, err
	}
	return s.Stacks.Plan(ctx, spec, runProgress)
}

func (s stacks) Provision(ctx context.Context, spec provider.StackSpec, runProgress progress.Log) (provider.StackResult, error) {
	facts, err := s.p.release().ReadEdgeFacts(ctx, spec)
	if err != nil {
		return provider.StackResult{}, err
	}
	if spec, err = s.p.functionImages().WithImages(ctx, spec); err != nil {
		return provider.StackResult{}, err
	}
	ctx = release.WithEdgeFacts(ctx, spec, facts)
	routeTableKey, err := s.p.objects().PutEdgeRouteTable(ctx, spec, runProgress)
	if err != nil {
		return provider.StackResult{}, err
	}
	result, err := s.Stacks.Provision(ctx, spec, runProgress)
	if err != nil {
		return result, err
	}
	result.RouteTableKey = routeTableKey
	result.PurgeSecret = facts.PurgeSecret
	if facts.OriginSecret.Present() {
		result.OriginSecretFingerprint = edge.OriginSecretFingerprint(facts.OriginSecret.Current)
	}
	return result, nil
}
