package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/gcp/provider/payloads"
)

func (p *Provider) ReadFunctionRuntime(_ context.Context, framework providerkit.Framework) ([]byte, error) {
	if !providerkit.BootsThroughRuntime(framework) {
		return nil, nil
	}
	return payloads.NodeRuntime(), nil
}
