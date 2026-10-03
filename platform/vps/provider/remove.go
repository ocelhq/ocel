package vps

import (
	"context"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

func (p *Provider) RemoveResource(ctx context.Context, ref provider.StackRef, binding provider.Binding, progress progress.Log) error {
	switch binding.Type {
	case provider.BindingPostgres:
		name := host.ResourceName(ref.Project, ref.Name.String(), binding.Name, postgresKind)
		if progress != nil {
			progress.Say("Removing postgres " + binding.Name + ", its container " + name + " and its data")
		}
		return p.host.RemoveResource(ctx, host.ResourceRef{Tier: ref.Tier, Project: ref.Project, Resource: binding.Name, Name: name})
	case provider.BindingKV:
		name := host.ResourceName(ref.Project, ref.Name.String(), binding.Name, kvKind)
		if progress != nil {
			progress.Say("Removing kv " + binding.Name + ", its container " + name + " and its data")
		}
		return p.host.RemoveResource(ctx, host.ResourceRef{Tier: ref.Tier, Project: ref.Project, Resource: binding.Name, Name: name})
	case provider.BindingTopic, provider.BindingTask:
		return p.removeTopic(ctx, ref, binding, progress)
	case provider.BindingBucket:
		return p.removeBucket(ctx, ref, binding, progress)
	case provider.BindingRealtime:
		return p.removeRealtime(ctx, ref, binding, progress)
	default:
		return nil
	}
}
