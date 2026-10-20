package aws

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/imageoptimizer"
)

func (p *Provider) ProgramEdge(ctx context.Context, req provider.EdgeProgramRequest) (edge.Program, error) {
	deployed, err := p.bootstrapped(ctx, req.Tier)
	if err != nil {
		return edge.Program{}, err
	}
	params, err := p.tierParams(ctx, req.Tier, req.Kind)
	if err != nil {
		return edge.Program{}, err
	}
	if params.EdgeCredentialsErr != nil {
		return edge.Program{}, fmt.Errorf("read the key the %s edge signs its requests to this account with: %w", req.Kind, params.EdgeCredentialsErr)
	}
	optimizer, err := imageoptimizer.ReadURL(ctx, p.partRecords(), req.Tier)
	if err != nil {
		return edge.Program{}, err
	}
	return edge.Program{
		Origin: deploy.OriginReach(deploy.OriginAccess{
			Region:            p.aws.Region,
			EdgeBucket:        deployed.EdgeBucket,
			AccessKeyID:       params.EdgeCredentials.AccessKeyID,
			SecretAccessKey:   params.EdgeCredentials.SecretAccessKey,
			ImageOptimizerURL: optimizer,
		}),
		ReleasesStore: edge.ReleasesStore{
			Name:                params.ReleasesStore.ScriptName,
			Endpoint:            params.ReleasesStore.Endpoint,
			BootstrapCredential: params.ReleasesStore.BootstrapCredential,
		},
	}, nil
}
