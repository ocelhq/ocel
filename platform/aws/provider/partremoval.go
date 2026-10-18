package aws

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/apigateway"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/aws/provider/bastion"
	"github.com/ocelhq/ocel/platform/aws/provider/cloudfrontpart"
	"github.com/ocelhq/ocel/platform/aws/provider/envsourcesync"
	"github.com/ocelhq/ocel/platform/aws/provider/imageoptimizer"
	"github.com/ocelhq/ocel/platform/aws/provider/notfoundapi"
	"github.com/ocelhq/ocel/platform/aws/provider/taginvalidator"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/containers"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/functionnetwork"
)

type removablePart struct {
	provider.Part
	plan   func(ctx context.Context, tier environment.Tier) (*provider.ChangeGroup, error)
	remove func(ctx context.Context, tier environment.Tier, progress progress.Log) error
}

var recordedParts = []provider.Part{
	{Name: imageoptimizer.Part, Summary: imageoptimizer.Summary},
	{Name: taginvalidator.Part, Summary: taginvalidator.Summary},
	{Name: cloudfrontpart.Part, Summary: cloudfrontpart.Summary},
	{Name: notfoundapi.Part, Summary: notfoundapi.Summary},
	{Name: envsourcesync.Part, Summary: envsourcesync.Summary},
}

func (p *Provider) removableParts() []removablePart {
	removable := make([]removablePart, 0, len(recordedParts)+3)
	for _, part := range recordedParts {
		removable = append(removable, removablePart{
			Part: part,
			plan: func(ctx context.Context, tier environment.Tier) (*provider.ChangeGroup, error) {
				return p.planPartRemoval(ctx, tier, part.Name, "this run removes it; the next deploy that uses it adds it again")
			},
			remove: func(ctx context.Context, tier environment.Tier, _ progress.Log) error {
				return p.removePart(ctx, tier, part.Name)
			},
		})
	}
	return append(removable,
		removablePart{
			Part: provider.Part{Name: containers.Part, Summary: containers.Summary},
			plan: p.planContainersRemoval,
			remove: func(ctx context.Context, tier environment.Tier, progress progress.Log) error {
				return p.stacks.Containers().Remove(ctx, tier, progress)
			},
		},
		removablePart{
			Part: provider.Part{Name: functionnetwork.Part, Summary: functionnetwork.Summary},
			plan: p.planFunctionNetworkRemoval,
			remove: func(ctx context.Context, tier environment.Tier, progress progress.Log) error {
				return p.stacks.FunctionNetwork().Remove(ctx, tier, progress)
			},
		},
		removablePart{
			Part:   provider.Part{Name: bastion.Part, Summary: bastion.Summary},
			plan:   p.planBastionRemoval,
			remove: p.removeBastion,
		},
	)
}

func (p *Provider) removeAddedParts(ctx context.Context, tier environment.Tier) error {
	for _, name := range addedParts {
		recorded, present, err := stackrecords.ReadBootstrapPart(ctx, p.KeyValues(), tier, name)
		if err != nil {
			return err
		}
		if !present {
			continue
		}
		if err := p.takePart(ctx, tier, name, recorded); err != nil {
			return err
		}
	}
	return nil
}

func (p *Provider) removePart(ctx context.Context, tier environment.Tier, name string) error {
	recorded, present, err := stackrecords.ReadBootstrapPart(ctx, p.KeyValues(), tier, name)
	if err != nil || !present {
		return err
	}
	return p.takePart(ctx, tier, name, recorded)
}

func (p *Provider) takePart(ctx context.Context, tier environment.Tier, name string, recorded stackrecords.BootstrapPart) error {
	namespace := string(p.namespace)
	switch name {
	case imageoptimizer.Part:
		return imageoptimizer.Remove(ctx, imageoptimizer.NewClients(p.aws), namespace, tier)
	case taginvalidator.Part:
		return taginvalidator.Remove(ctx, taginvalidator.NewClients(p.aws), namespace, tier)
	case cloudfrontpart.Part:
		api, err := p.cloudFrontAPI(ctx)
		if err != nil {
			return err
		}
		return cloudfrontpart.Remove(ctx, api, cloudfrontpart.Spec{Namespace: p.namespace, Tier: tier}, recorded.Resources)
	case notfoundapi.Part:
		return notfoundapi.Remove(ctx, apigateway.NewFromConfig(p.aws), recorded.Resources)
	case envsourcesync.Part:
		return envsourcesync.Remove(ctx, envsourcesync.NewClients(p.aws), namespace, tier)
	}
	return fmt.Errorf("this provider removes no bootstrap part named %q", name)
}

func (p *Provider) planAddedPartRemoval(ctx context.Context, tier environment.Tier) ([]provider.ChangeGroup, error) {
	var groups []provider.ChangeGroup
	for _, name := range addedParts {
		group, err := p.planPartRemoval(ctx, tier, name, "the bootstrap it belongs to is being removed")
		if err != nil {
			return nil, err
		}
		if group != nil {
			groups = append(groups, *group)
		}
	}
	return groups, nil
}

func (p *Provider) planPartRemoval(ctx context.Context, tier environment.Tier, name, why string) (*provider.ChangeGroup, error) {
	recorded, present, err := stackrecords.ReadBootstrapPart(ctx, p.KeyValues(), tier, name)
	if err != nil || !present {
		return nil, err
	}
	return addedPartDeletion(name, why, recorded), nil
}

func addedPartDeletion(name, why string, recorded stackrecords.BootstrapPart) *provider.ChangeGroup {
	by := recorded.AddedBy
	switch {
	case recorded.AddedWith != "" && by != "":
		by = "`" + recorded.AddedWith + "` for " + by
	case recorded.AddedWith != "":
		by = "`" + recorded.AddedWith + "`"
	}
	return partDeletion(name, "added by "+by+" on first use; "+why, recorded.Resources)
}

func (p *Provider) planContainersRemoval(ctx context.Context, tier environment.Tier) (*provider.ChangeGroup, error) {
	part, present, err := containers.ReadPart(ctx, p.KeyValues(), p.aws.Region, tier)
	if err != nil || !present {
		return nil, err
	}
	if len(part.UsedBy) > 0 {
		return nil, containers.RefuseInUse(tier, part.UsedBy)
	}
	return partDeletion(part.Name, "this run removes it; the next container deploy adds it again", part.Resources), nil
}

func (p *Provider) planFunctionNetworkRemoval(ctx context.Context, tier environment.Tier) (*provider.ChangeGroup, error) {
	part, present, err := functionnetwork.ReadPart(ctx, p.KeyValues(), p.aws.Region, tier)
	if err != nil || !present {
		return nil, err
	}
	if len(part.UsedBy) > 0 {
		return nil, functionnetwork.RefuseInUse(tier, part.UsedBy)
	}
	return partDeletion(part.Name, "this run removes it; the next deploy of a serverless app that reaches a postgres or kv Ocel provisions adds it again", part.Resources), nil
}

func (p *Provider) planBastionRemoval(ctx context.Context, tier environment.Tier) (*provider.ChangeGroup, error) {
	resources, found, err := bastion.Describe(ctx, bastion.NewClients(p.aws), tier)
	if err != nil || !found {
		return nil, err
	}
	return partDeletion(bastion.Part, "this run removes it; the next port forward adds it again", resources), nil
}

func (p *Provider) removeBastion(ctx context.Context, tier environment.Tier, _ progress.Log) error {
	return bastion.Remove(ctx, bastion.NewClients(p.aws), tier)
}

func partDeletion(name, reason string, resources []provider.PartResource) *provider.ChangeGroup {
	group := provider.ChangeGroup{
		Kind:   provider.StackGroupKind,
		Name:   "bootstrap part " + name,
		Part:   name,
		Action: provider.ActionDelete,
		Reason: reason,
	}
	for _, resource := range resources {
		group.Changes = append(group.Changes, provider.Change{Kind: resource.Kind, Name: resource.ID, Action: provider.ActionDelete})
	}
	return &group
}
