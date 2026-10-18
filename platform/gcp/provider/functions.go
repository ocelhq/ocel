package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/compute/functions"
)

func (p *Provider) functions() functions.Functions { return functions.Functions{Release: p.release()} }

func (p *Provider) ProvisionFunctions(ctx context.Context, spec provider.StackSpec, progress progress.Log) ([]provider.Function, error) {
	return p.functions().Provision(ctx, spec, progress)
}

func (p *Provider) RemoveFunctions(ctx context.Context, ref provider.StackRef, functions []provider.Function, images provider.ImageStore, progress progress.Log) error {
	return p.functions().Remove(ctx, ref, functions, images, progress)
}

func (p *Provider) NameFunctions(ctx context.Context, spec provider.StackSpec) ([]provider.Function, error) {
	return p.functions().Name(ctx, spec)
}

func (p *Provider) RemoveFunctionRevisions(ctx context.Context, ref provider.StackRef, functions []provider.Function, images provider.ImageStore, progress progress.Log) ([]provider.Function, error) {
	return p.functions().RemoveRevisions(ctx, ref, functions, images, progress)
}
