package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
)

func (p *Provider) RemoveResource(ctx context.Context, ref provider.StackRef, binding provider.Binding, progress progress.Log) error {
	if binding.Type != provider.BindingKV {
		return nil
	}
	return p.removeKV(ctx, ref, binding, progress)
}
