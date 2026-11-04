package aws

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/platform/aws/provider/tier/imageoptimizer"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/sharedparts"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/taginvalidator"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/platform/aws/provider/bastion"
	"github.com/ocelhq/ocel/platform/aws/provider/control"
	"github.com/ocelhq/ocel/platform/aws/provider/tier"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/containers"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/functionnetwork"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/network"
)

func (p *Provider) tierParts(records tier.Records) tier.Parts {
	namespace := string(p.Namespace)
	region := p.Config.Region
	return p.TierParts(records, control.EngineParts{
		Bastion: bastion.NewPart(bastion.NewClients(p.Config), namespace, region, p.releaseBastionNetwork),
		SharedStacks: tier.Parts{
			containers.NewPart(p.stacks.Containers(), records.KeyValues, namespace, region),
			functionnetwork.NewPart(p.stacks.FunctionNetwork(), records.KeyValues, region),
			network.NewPart(p.stacks.Network(), records.KeyValues, region),
		},
		ImageOptimizer: imageoptimizer.NewPart(records, imageoptimizer.NewClients(p.Config), namespace, p.ImageOptimizerSpec),
		TagInvalidator: taginvalidator.NewPart(records, taginvalidator.NewClients(p.Config), namespace, p.TagInvalidatorSpec),
	})
}

func (p *Provider) planDeployParts(ctx context.Context, pre provider.DeployPreflight) ([]provider.ChangeGroup, error) {
	tier := pre.Deploy.Tier
	deployed, err := p.ReadDeployed(ctx, tier)
	if err != nil || !deployed.Present {
		return nil, err
	}
	parts := p.tierParts(p.PartRecords())
	used := parts.AddedFor(pre.Uses, pre.Edge)
	var groups []provider.ChangeGroup
	for _, part := range used {
		group, err := part.Added.Plan(ctx, tier)
		if err != nil {
			return nil, err
		}
		if group != nil {
			groups = append(groups, *group)
		}
	}
	return groups, nil
}

func (p *Provider) planNetworkRetiredWith(ctx context.Context, at environment.Tier, removed []string) (*provider.ChangeGroup, error) {
	return planNetworkRetiredWith(ctx, p.KeyValues(), p.Config.Region, p.stacks.Network().PartsByConsumer, at, removed)
}

func planNetworkRetiredWith(ctx context.Context, store records.Store, region string, partsByConsumer map[string]string, at environment.Tier, removed []string) (*provider.ChangeGroup, error) {
	part, present, err := sharedparts.ReadNetworkPart(ctx, store, region, at)
	if err != nil || !present || len(part.UsedBy) == 0 {
		return nil, err
	}
	for _, user := range part.UsedBy {
		if leaving, isPart := partsByConsumer[user]; !isPart || !slices.Contains(removed, leaving) {
			return nil, nil
		}
	}
	return tier.Deletion(sharedparts.NetworkPart, "the last part that runs in it is removed by this run, and the network goes with it", part.Resources), nil
}

func (p *Provider) releaseBastionNetwork(ctx context.Context, tier environment.Tier, progress progress.Log) error {
	return p.stacks.Network().Release(ctx, p.KeyValues(), bastion.ConsumerRef(tier), progress)
}
