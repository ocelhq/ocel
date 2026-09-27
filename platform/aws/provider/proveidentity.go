package aws

import (
	"context"

	"github.com/ocelhq/ocel/pkg/envsource"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
)

func (p *Provider) ProveIdentity(ctx context.Context, audience string) (envsource.IdentityProof, error) {
	return awsports.CallerIdentity{Config: p.aws}.Prove(ctx, audience)
}
