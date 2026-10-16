package aws

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/aws/provider/envsourcesync"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
)

func (p *Provider) envSourceSyncSpec(ctx context.Context, tier environment.Tier) (envsourcesync.Spec, error) {
	deployed, err := p.bootstrapped(ctx, tier)
	if err != nil {
		return envsourcesync.Spec{}, err
	}
	if err := p.requireBootstrapped(deployed, tier); err != nil {
		return envsourcesync.Spec{}, err
	}
	return envsourcesync.Spec{
		Namespace: string(p.namespace),
		Tier:      tier,
		Region:    p.aws.Region,
		Boundary:  deployed.AppBoundaryARN,
		RolePath:  p.namespace.AppRolePathFor(tier),
		Table:     deployed.VariablesTable,
		TableARN:  deployed.VariablesTableARN,
		Key:       deployed.VariablesKeyARN,
	}, nil
}

func (p *Provider) EnsureEnvSourceSync(ctx context.Context, tier environment.Tier, project string) error {
	work, keyed, err := p.envSourceSyncWork(ctx, tier, project)
	if err != nil {
		return err
	}
	if !keyed {
		return awsports.RefuseUnkeyed(tier)
	}
	return stackrecords.EnsureBootstrapPart(ctx, p.KeyValues(), tier, work)
}

func (p *Provider) envSourceSyncWork(ctx context.Context, tier environment.Tier, project string) (stackrecords.PartWork, bool, error) {
	spec, err := p.envSourceSyncSpec(ctx, tier)
	if err != nil || spec.Key == "" {
		return stackrecords.PartWork{}, false, err
	}
	return stackrecords.PartWork{
		Name:      envsourcesync.Part,
		WrittenBy: string(p.writtenBy),
		Digest:    envsourcesync.Digest(spec),
		AddedBy:   project,
		Make: func(ctx context.Context) ([]provider.PartResource, error) {
			return envsourcesync.Ensure(ctx, envsourcesync.NewClients(p.aws), spec)
		},
	}, true, nil
}

func (p *Provider) envSourceSyncPart(ctx context.Context, tier environment.Tier) (provider.BootstrapPart, bool, error) {
	recorded, present, err := stackrecords.ReadBootstrapPart(ctx, p.KeyValues(), tier, envsourcesync.Part)
	if err != nil || !present {
		return provider.BootstrapPart{}, false, err
	}
	spec, err := p.envSourceSyncSpec(ctx, tier)
	if err != nil {
		return provider.BootstrapPart{}, false, err
	}
	users, err := envsource.ScheduledProjects(ctx, p.KeyValues(), tier)
	if err != nil {
		return provider.BootstrapPart{}, false, err
	}
	return provider.BootstrapPart{
		Name:      envsourcesync.Part,
		Summary:   envsourcesync.Summary,
		Current:   recorded.CurrentFor(string(p.writtenBy), envsourcesync.Digest(spec)),
		Digest:    recorded.Digest,
		Resources: envsourcesync.Resources(spec.Namespace, tier),
		UsedBy:    users,
		Shape:     &provider.PartShape{Vendor: string(Vendor), Region: p.aws.Region, Resources: envsourcesync.Shape(spec.Namespace, tier)},
	}, true, nil
}
