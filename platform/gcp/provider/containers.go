package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/compute/containers"
)

func (p *Provider) containers() containers.Containers {
	return containers.Containers{Release: p.release()}
}

func (p *Provider) ProvisionContainers(ctx context.Context, spec provider.StackSpec, progress progress.Log) ([]provider.AppContainer, error) {
	return p.containers().Provision(ctx, spec, progress)
}

func (p *Provider) RemoveContainers(ctx context.Context, ref provider.StackRef, containers []provider.AppContainer, images provider.ImageStore, progress progress.Log) error {
	return p.containers().Remove(ctx, ref, containers, images, progress)
}

func (p *Provider) NameContainers(ctx context.Context, spec provider.StackSpec) ([]provider.AppContainer, error) {
	return p.containers().Name(ctx, spec)
}

func (p *Provider) RemoveContainerRevisions(ctx context.Context, ref provider.StackRef, containers []provider.AppContainer, images provider.ImageStore, progress progress.Log) ([]provider.AppContainer, error) {
	return p.containers().RemoveRevisions(ctx, ref, containers, images, progress)
}
