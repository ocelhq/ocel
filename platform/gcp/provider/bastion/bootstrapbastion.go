package bastion

import (
	"context"

	runv2 "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/cost"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
)

const reasonBastionOnDemand = "made by a build's first port forward"

var bastionAPIs = []string{"run.googleapis.com"}

func readBastion(ctx context.Context, b bootstrap.Engine, tier environment.Tier) (*runv2.GoogleCloudRunV2Service, error) {
	off, err := b.ListServicesOff(ctx, bastionAPIs)
	if err != nil || len(off) > 0 {
		return nil, err
	}
	return Bastion{clients: b.Clients}.read(ctx, tier)
}

func bastionRemovals(ctx context.Context, b bootstrap.Engine, tier environment.Tier) ([]provider.Change, error) {
	var changes []provider.Change
	service, err := readBastion(ctx, b, tier)
	if err != nil {
		return nil, err
	}
	if service != nil {
		changes = append(changes, provider.Change{Kind: string(bootstrap.KindService), Name: b.Clients.Bastion(tier), Action: provider.ActionDelete, Reason: reasonBastionOnDemand})
	}
	account, err := b.Clients.AccountExists(ctx, b.Clients.BastionAccount(tier))
	if err != nil {
		return nil, err
	}
	if account {
		changes = append(changes, provider.Change{Kind: string(bootstrap.KindServiceAccount), Name: b.Clients.BastionAccount(tier), Action: provider.ActionDelete, Reason: reasonBastionOnDemand})
	}
	return changes, nil
}

func removeBastion(ctx context.Context, b bootstrap.Engine, tier environment.Tier, progress progress.Log) error {
	at := Bastion{clients: b.Clients, tearDown: b.TearDown}
	off, err := b.ListServicesOff(ctx, bastionAPIs)
	if err != nil {
		return err
	}
	if len(off) > 0 {
		return at.removeAccount(ctx, tier, progress)
	}
	return at.remove(ctx, tier, progress)
}

const (
	PartName       = "bastion"
	bastionSummary = "the service a port forward runs inside the tier's network"
)

var Part = bootstrap.RemovablePart{
	Part:     provider.Part{Name: PartName, Summary: bastionSummary},
	Plan:     bastionRemovals,
	Remove:   removeBastion,
	Take:     removeBastion,
	Describe: describeBastionPart,
}

func describeBastionPart(ctx context.Context, b bootstrap.Engine, tier environment.Tier) (provider.BootstrapPart, bool, error) {
	service, err := readBastion(ctx, b, tier)
	if err != nil || service == nil {
		return provider.BootstrapPart{}, false, err
	}
	return provider.BootstrapPart{
		Name:    PartName,
		Summary: bastionSummary,
		Current: true,
		Resources: []provider.PhysicalResource{
			{Kind: string(bootstrap.KindService), ID: b.Clients.Bastion(tier)},
			{Kind: string(bootstrap.KindServiceAccount), ID: b.Clients.BastionAccount(tier)},
		},
		Shape: &provider.PartShape{Vendor: string(gcloud.Vendor), Region: b.Clients.Region, Resources: []pricing.Shaped{{
			Name:       b.Clients.Bastion(tier),
			Type:       cost.CloudRunServiceType,
			Properties: run.PricedProperties(true, 0, run.RevisionMemory, run.IngressInternal),
		}}},
	}, true, nil
}
