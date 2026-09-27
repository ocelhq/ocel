package vps

import (
	"context"

	"github.com/ocelhq/ocel/pkg/provider"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

func (p *Provider) RemoveResource(ctx context.Context, ref provider.StackRef, binding provider.Binding, progress edge.Progress) error {
	switch binding.Type {
	case provider.BindingPostgres:
		name := host.ResourceName(ref.Name.String(), binding.Name, postgresKind)
		if progress != nil {
			progress.Say("Removing postgres " + binding.Name + ", its container " + name + " and its data")
		}
		return p.host.RemoveResource(ctx, host.ResourceRef{Class: ref.Class, Project: ref.Project, Resource: binding.Name, Name: name})
	case provider.BindingBucket:
		return p.removeBucket(ctx, ref, binding, progress)
	default:
		return nil
	}
}
