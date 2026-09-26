package aws

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
)

func (p *Provider) VerifyGrants(_ context.Context, binding providerkit.Binding) error {
	return deploy.VerifyGrants(binding)
}
