package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

func (p *Provider) evaluateFront(ctx context.Context, tier environment.Tier) (transform.Result, error) {
	if evaluated, done := p.evaluatedFronts.Load(tier); done {
		return evaluated.(transform.Result), nil
	}
	evaluated, err := alb.EvaluateFront(ctx, p.transforms, tier)
	if err != nil {
		return transform.Result{}, err
	}
	p.evaluatedFronts.Store(tier, evaluated)
	return evaluated, nil
}

func (p *Provider) evaluateAppEdge(ctx context.Context, spec provider.StackSpec) (transform.Result, error) {
	return alb.EvaluateApp(ctx, p.transforms, spec)
}

func (p *Provider) recordAppEdge(spec provider.StackSpec, evaluated transform.Result) {
	if spec.App == nil || spec.Edge == nil || spec.Edge.Kind() != alb.Kind {
		return
	}
	p.appEdges.Set(spec.Ref.Tier, spec.Ref.Project, spec.App.App, evaluated)
}
