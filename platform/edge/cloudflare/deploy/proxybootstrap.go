package cloudflare

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
)

func (x *Proxy) Bootstrap(context.Context, environment.Tier) (edge.BootstrapOutput, error) {
	return edge.BootstrapOutput{Trust: edge.TrustExternal}, nil
}

func (x *Proxy) Teardown(context.Context, environment.Tier) error { return nil }
