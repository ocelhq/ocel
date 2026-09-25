package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/gcp/provider/payloads"
)

func (p *Provider) FunctionRuntime(_ context.Context, runtime providerkit.Runtime) ([]byte, error) {
	if !providerkit.BootsThroughRuntime(runtime) {
		return nil, nil
	}
	return payloads.NodeRuntime(), nil
}
