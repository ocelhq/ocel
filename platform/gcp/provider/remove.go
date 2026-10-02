package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
)

func (p *Provider) RemoveResource(ctx context.Context, ref provider.StackRef, binding provider.Binding, progress progress.Log) error {
	switch binding.Type {
	case provider.BindingKV:
		return p.removeKV(ctx, ref, binding, progress)
	case provider.BindingTopic, provider.BindingTask:
		return p.removeTopic(ctx, ref, binding, progress)
	}
	return nil
}
