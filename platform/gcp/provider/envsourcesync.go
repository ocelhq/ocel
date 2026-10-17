package gcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

const (
	envSourceSyncPart    = "env-source-sync"
	envSourceSyncSummary = "the service Cloud Scheduler calls once a minute to read each scheduled env source of the tier and write what changed"

	reasonSyncOnDemand = "the tier's first scheduled env source made it, so the bootstrap's own list does not name it"
)

func (p *Provider) EnsureEnvSourceSync(ctx context.Context, tier environment.Tier, project string) error {
	b, err := bootstrapGate{p}.openBootstrap(ctx)
	if err != nil {
		return err
	}
	return b.ensureEnvSourceSync(ctx, tier, project)
}

func (b bootstrap) syncDigest(tier environment.Tier) string {
	s := b.syncServing(tier)
	env := make([]string, 0, len(s.env))
	for _, key := range slices.Sorted(maps.Keys(s.env)) {
		env = append(env, key+"="+s.env[key])
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{
		s.service, s.image, s.account, s.cpu, s.timeout.String(), syncGeneration, syncSchedule, scheduleTimeZone, strings.Join(env, ","),
	}, "\n")))
	return hex.EncodeToString(sum[:])
}

func (b bootstrap) syncResources(tier environment.Tier) []provider.PartResource {
	name := b.clients.EnvSourceSync(tier)
	return []provider.PartResource{{Kind: string(KindService), ID: name}, {Kind: string(KindSchedule), ID: name}}
}

func (b bootstrap) ensureEnvSourceSync(ctx context.Context, tier environment.Tier, project string) error {
	return stackrecords.EnsureBootstrapPart(ctx, b.records, tier, b.envSourceSyncWork(tier, project))
}

func (b bootstrap) envSourceSyncWork(tier environment.Tier, project string) stackrecords.PartWork {
	return stackrecords.PartWork{
		Name:      envSourceSyncPart,
		WrittenBy: string(b.writtenBy),
		Digest:    b.syncDigest(tier),
		AddedBy:   project,
		Make: func(ctx context.Context) ([]provider.PartResource, error) {
			if err := b.mendEnvSourceSync(ctx, tier); err != nil {
				return nil, err
			}
			return b.syncResources(tier), nil
		},
	}
}

func (b bootstrap) mendEnvSourceSync(ctx context.Context, tier environment.Tier) error {
	name := b.clients.EnvSourceSync(tier)
	service, err := b.servicePresence(ctx, tier, name)
	if err != nil {
		return err
	}
	if !service.present || service.mends != "" {
		if err := b.makeService(ctx, tier, name); err != nil {
			return err
		}
	}
	schedule, err := b.schedulePresence(ctx, tier, name)
	if err != nil || (schedule.present && schedule.mends == "") {
		return err
	}
	return b.makeSchedule(ctx, tier, name)
}

func syncShape(name, region string) []pricing.Shaped {
	return []pricing.Shaped{
		{Name: name, Type: itemTypes[KindService], Properties: itemProperties(item{Kind: KindService, Name: name}, region)},
		{Name: name, Type: itemTypes[KindSchedule], Properties: itemProperties(item{Kind: KindSchedule, Name: name}, region)},
	}
}

func (b bootstrap) envSourceSyncPart(ctx context.Context, tier environment.Tier) (provider.BootstrapPart, bool, error) {
	recorded, present, err := stackrecords.ReadBootstrapPart(ctx, b.records, tier, envSourceSyncPart)
	if err != nil || !present {
		return provider.BootstrapPart{}, false, err
	}
	users, err := envsource.ScheduledProjects(ctx, b.records, tier)
	if err != nil {
		return provider.BootstrapPart{}, false, err
	}
	return provider.BootstrapPart{
		Name:      envSourceSyncPart,
		Summary:   envSourceSyncSummary,
		Current:   recorded.CurrentFor(string(b.writtenBy), b.syncDigest(tier)),
		Digest:    recorded.Digest,
		Resources: b.syncResources(tier),
		UsedBy:    users,
		Shape:     &provider.PartShape{Vendor: string(Vendor), Region: b.clients.region, Resources: syncShape(b.clients.EnvSourceSync(tier), b.clients.region)},
	}, true, nil
}

func (b bootstrap) envSourceSyncRemovals(ctx context.Context, tier environment.Tier) ([]provider.Change, error) {
	name := b.clients.EnvSourceSync(tier)
	var changes []provider.Change
	schedule, err := b.schedulePresence(ctx, tier, name)
	if err != nil {
		return nil, err
	}
	if schedule.present {
		changes = append(changes, provider.Change{Kind: string(KindSchedule), Name: name, Action: provider.ActionDelete, Reason: reasonSyncOnDemand})
	}
	service, err := b.readService(ctx, name)
	if err != nil {
		return nil, err
	}
	if service != nil {
		changes = append(changes, provider.Change{Kind: string(KindService), Name: name, Action: provider.ActionDelete, Reason: reasonSyncOnDemand})
	}
	return changes, nil
}

func (b bootstrap) removeEnvSourceSync(ctx context.Context, tier environment.Tier, progress progress.Log) error {
	name := b.clients.EnvSourceSync(tier)
	if err := b.takeSchedule(ctx, name); err != nil {
		return err
	}
	service, err := b.readService(ctx, name)
	if err != nil || service == nil {
		return err
	}
	if err := b.takeService(ctx, name); err != nil {
		return err
	}
	ensureProgress(progress).Say("Removed the env source sync " + name)
	return nil
}
