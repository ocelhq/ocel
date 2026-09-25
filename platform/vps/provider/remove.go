package vps

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

func (p *Provider) RemoveResource(ctx context.Context, ref providerkit.StackRef, binding providerkit.Binding, report providerkit.Reporter) error {
	switch binding.Type {
	case providerkit.BindingPostgres:
		name := host.ResourceName(ref.Name.String(), binding.Name, postgresKind)
		if report != nil {
			report.Say("Taking postgres " + binding.Name + " and its data down")
		}
		return p.host.RemoveResource(ctx, host.ResourceRef{Class: ref.Class, Project: ref.Project, Resource: binding.Name, Name: name})
	case providerkit.BindingBucket:
		return p.removeBucket(ctx, ref, binding, report)
	default:
		return nil
	}
}
