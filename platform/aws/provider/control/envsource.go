package control

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envsource"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/envsourcesync"
)

func (p *Provider) ProveIdentity(ctx context.Context, audience string) (envsource.IdentityProof, error) {
	return awsports.CallerIdentity{Config: p.Config}.Prove(ctx, audience)
}

func (p *Provider) envSourceSyncSpec(ctx context.Context, tier environment.Tier) (envsourcesync.Spec, error) {
	deployed, err := p.ReadDeployed(ctx, tier)
	if err != nil {
		return envsourcesync.Spec{}, err
	}
	if err := deployed.RefuseUnbootstrapped(tier); err != nil {
		return envsourcesync.Spec{}, err
	}
	return envsourcesync.Spec{
		Namespace: string(p.Namespace),
		Tier:      tier,
		Region:    p.Config.Region,
		Boundary:  deployed.AppBoundaryARN,
		RolePath:  p.Namespace.AppRolePathFor(tier),
		Table:     deployed.VariablesTable,
		TableARN:  deployed.VariablesTableARN,
		Key:       deployed.VariablesKeyARN,
	}, nil
}

func (p *Provider) EnsureEnvSourceSync(ctx context.Context, tier environment.Tier, project string) error {
	spec, err := p.envSourceSyncSpec(ctx, tier)
	if err != nil {
		return err
	}
	return envsourcesync.EnsurePart(ctx, p.PartRecords(), envsourcesync.NewClients(p.Config), spec, project)
}
