package envsourcesync

import (
	"context"
	"errors"
	"fmt"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/tier"
)

func NewPart(records tier.Records, c Clients, namespace string, specFor func(context.Context, environment.Tier) (Spec, error)) tier.Part {
	return tier.Part{
		Name:    Part,
		Summary: Summary,
		Describe: func(ctx context.Context, tier environment.Tier) (provider.BootstrapPart, bool, error) {
			return describe(ctx, records, specFor, tier)
		},
		Inventory: func(tier environment.Tier) []pricing.Resource { return Inventory(namespace, tier) },
		Removal: records.Removal(Part, func(ctx context.Context, tier environment.Tier, _ []provider.PhysicalResource) error {
			return Remove(ctx, c, namespace, tier)
		}),
		Teardown: &tier.Removal{
			Plan: func(ctx context.Context, at environment.Tier) (*provider.ChangeGroup, error) {
				_, present, err := stackrecords.ReadBootstrapPart(ctx, records.KeyValues, at, Part)
				if err != nil || !present {
					return nil, err
				}
				return tier.Deletion(Part, "the bootstrap it belongs to is being removed", Resources(namespace, at)), nil
			},
			Remove: func(ctx context.Context, tier environment.Tier, progress progress.Log) error {
				if progress != nil {
					progress.Say(fmt.Sprintf("Removing the %s tier's env source sync (Lambda, EventBridge Scheduler, IAM)", tier))
				}
				return Remove(ctx, c, namespace, tier)
			},
		},
		Repair: records.Repair(Part, func(ctx context.Context, tier environment.Tier) (work tier.Work, keyed bool, err error) {
			spec, err := specFor(ctx, tier)
			if err != nil || spec.Key == "" {
				return work, false, err
			}
			return Work(c, spec), true, nil
		}),
		Use: &tier.Use{IsUsedBy: isUsedByEveryDeploy},
	}
}

func isUsedByEveryDeploy([]buildoutput.Uses, edge.Kind) bool { return true }

func Work(c Clients, spec Spec) tier.Work {
	return tier.Work{
		Digest: Digest(spec),
		Make: func(ctx context.Context, _ []provider.PhysicalResource, updatesTrust bool) ([]provider.PhysicalResource, error) {
			spec := spec
			spec.UpdatesTrust = updatesTrust
			return Ensure(ctx, c, spec)
		},
	}
}

func EnsurePart(ctx context.Context, records tier.Records, c Clients, spec Spec, project string) error {
	if spec.Key == "" {
		return awsports.RefuseUnkeyed(spec.Tier)
	}
	work := Work(c, spec)
	err := stackrecords.EnsureBootstrapPart(ctx, records.KeyValues, spec.Tier, stackrecords.PartWork{
		Name:      Part,
		WrittenBy: string(records.WrittenBy),
		Digest:    work.Digest,
		AddedBy:   project,
		Make: func(ctx context.Context) ([]provider.PhysicalResource, error) {
			return work.Make(ctx, nil, false)
		},
	})
	if err != nil && !errors.Is(err, stackrecords.ErrPartBehind) {
		return err
	}
	return nil
}

func describe(ctx context.Context, records tier.Records, specFor func(context.Context, environment.Tier) (Spec, error), tier environment.Tier) (provider.BootstrapPart, bool, error) {
	recorded, present, err := stackrecords.ReadBootstrapPart(ctx, records.KeyValues, tier, Part)
	if err != nil || !present {
		return provider.BootstrapPart{}, false, err
	}
	spec, err := specFor(ctx, tier)
	if err != nil {
		return provider.BootstrapPart{}, false, err
	}
	users, err := envsource.ScheduledProjects(ctx, records.KeyValues, tier)
	if err != nil {
		return provider.BootstrapPart{}, false, err
	}
	return provider.BootstrapPart{
		Name:      Part,
		Summary:   Summary,
		Current:   recorded.CurrentFor(string(records.WrittenBy), Digest(spec)),
		Digest:    recorded.Digest,
		Resources: Resources(spec.Namespace, tier),
		UsedBy:    users,
		Inventory: records.Inventory(Inventory(spec.Namespace, tier)),
	}, true, nil
}
