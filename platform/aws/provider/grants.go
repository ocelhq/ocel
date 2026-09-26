package aws

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
)

func (p *Provider) VerifyGrants(_ context.Context, binding provider.Binding) error {
	return deploy.VerifyGrants(binding)
}
