package envsource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/cost"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
)

const (
	PartName             = "env-source-sync"
	envSourceSyncSummary = "the service Cloud Scheduler calls once a minute to read each scheduled env source of the tier and write what changed"

	reasonSyncOnDemand = "the tier's first scheduled env source made it, so the bootstrap's own list does not name it"
)

func syncDigest(b bootstrap.Engine, tier environment.Tier) string {
	s := SyncServing(b, tier)
	env := make([]string, 0, len(s.Env))
	for _, key := range slices.Sorted(maps.Keys(s.Env)) {
		env = append(env, key+"="+s.Env[key])
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{
		s.Service, s.Image, s.Account, s.CPU, s.Timeout.String(), syncGeneration, syncSchedule, gcloud.ScheduleTimeZone, strings.Join(env, ","),
	}, "\n")))
	return hex.EncodeToString(sum[:])
}

func syncResources(b bootstrap.Engine, tier environment.Tier) []provider.PartResource {
	name := b.Clients.EnvSourceSync(tier)
	return []provider.PartResource{{Kind: string(bootstrap.KindService), ID: name}, {Kind: string(bootstrap.KindSchedule), ID: name}}
}

func Ensure(ctx context.Context, b bootstrap.Engine, tier environment.Tier, project string) error {
	return stackrecords.EnsureBootstrapPart(ctx, b.Records, tier, envSourceSyncWork(b, tier, project))
}

func envSourceSyncWork(b bootstrap.Engine, tier environment.Tier, project string) stackrecords.PartWork {
	return stackrecords.PartWork{
		Name:      PartName,
		WrittenBy: string(b.WrittenBy),
		Digest:    syncDigest(b, tier),
		AddedBy:   project,
		Make: func(ctx context.Context) ([]provider.PartResource, error) {
			if err := mendEnvSourceSync(ctx, b, tier); err != nil {
				return nil, err
			}
			return syncResources(b, tier), nil
		},
	}
}

func mendEnvSourceSync(ctx context.Context, b bootstrap.Engine, tier environment.Tier) error {
	name := b.Clients.EnvSourceSync(tier)
	service, err := servicePresence(ctx, b, tier, name)
	if err != nil {
		return err
	}
	if !service.Present || service.Mends != "" {
		if err := makeService(ctx, b, tier, name); err != nil {
			return err
		}
	}
	schedule, err := schedulePresence(ctx, b, tier, name)
	if err != nil || (schedule.Present && schedule.Mends == "") {
		return err
	}
	return makeSchedule(ctx, b, tier, name)
}

func Shape(name, region string) []pricing.Shaped {
	return []pricing.Shaped{
		{Name: name, Type: cost.CloudRunServiceType, Properties: ServiceProperties(region)},
		{Name: name, Type: cost.SchedulerJobType, Properties: map[string]any{"region": region, "schedule": syncSchedule}},
	}
}

func describeEnvSourceSyncPart(ctx context.Context, b bootstrap.Engine, tier environment.Tier) (provider.BootstrapPart, bool, error) {
	recorded, present, err := stackrecords.ReadBootstrapPart(ctx, b.Records, tier, PartName)
	if err != nil || !present {
		return provider.BootstrapPart{}, false, err
	}
	users, err := envsource.ScheduledProjects(ctx, b.Records, tier)
	if err != nil {
		return provider.BootstrapPart{}, false, err
	}
	return provider.BootstrapPart{
		Name:      PartName,
		Summary:   envSourceSyncSummary,
		Current:   recorded.CurrentFor(string(b.WrittenBy), syncDigest(b, tier)),
		Digest:    recorded.Digest,
		Resources: syncResources(b, tier),
		UsedBy:    users,
		Shape:     &provider.PartShape{Vendor: string(gcloud.Vendor), Region: b.Clients.Region, Resources: Shape(b.Clients.EnvSourceSync(tier), b.Clients.Region)},
	}, true, nil
}

func envSourceSyncRemovals(ctx context.Context, b bootstrap.Engine, tier environment.Tier) ([]provider.Change, error) {
	name := b.Clients.EnvSourceSync(tier)
	var changes []provider.Change
	schedule, err := schedulePresence(ctx, b, tier, name)
	if err != nil {
		return nil, err
	}
	if schedule.Present {
		changes = append(changes, provider.Change{Kind: string(bootstrap.KindSchedule), Name: name, Action: provider.ActionDelete, Reason: reasonSyncOnDemand})
	}
	service, err := readService(ctx, b, name)
	if err != nil {
		return nil, err
	}
	if service != nil {
		changes = append(changes, provider.Change{Kind: string(bootstrap.KindService), Name: name, Action: provider.ActionDelete, Reason: reasonSyncOnDemand})
	}
	return changes, nil
}

func removeEnvSourceSync(ctx context.Context, b bootstrap.Engine, tier environment.Tier, progress progress.Log) error {
	name := b.Clients.EnvSourceSync(tier)
	if err := gcloud.TakeSchedule(ctx, b.Clients, name); err != nil {
		return err
	}
	service, err := readService(ctx, b, name)
	if err != nil || service == nil {
		return err
	}
	if err := takeService(ctx, b, name); err != nil {
		return err
	}
	gcloud.EnsureProgress(progress).Say("Removed the env source sync " + name)
	return nil
}

func ServiceProperties(region string) map[string]any {
	return map[string]any{
		"location":                   region,
		"ingress":                    run.IngressInternal,
		"requests_per_hour":          syncRequestsPerHour,
		"billed_seconds_per_request": syncBilledSecondsEach,
		"template": map[string]any{
			"scaling": map[string]any{"min_instance_count": 0, "max_instance_count": syncInstances},
			"containers": []any{map[string]any{
				"resources": map[string]any{
					"cpu_idle": true,
					"limits":   map[string]any{"cpu": syncCPU, "memory": strconv.Itoa(syncMemoryMiB) + "Mi"},
				},
			}},
		},
	}
}

func removeEnvSourceSyncPart(ctx context.Context, b bootstrap.Engine, tier environment.Tier, progress progress.Log) error {
	if err := removeEnvSourceSync(ctx, b, tier, progress); err != nil {
		return err
	}
	return stackrecords.ForgetBootstrapPart(ctx, b.Records, tier, PartName)
}

var Part = bootstrap.RemovablePart{
	Part:     provider.Part{Name: PartName, Summary: envSourceSyncSummary},
	Plan:     envSourceSyncRemovals,
	Remove:   removeEnvSourceSyncPart,
	Take:     removeEnvSourceSync,
	Describe: describeEnvSourceSyncPart,
	Work: func(b bootstrap.Engine, tier environment.Tier) stackrecords.PartWork {
		return envSourceSyncWork(b, tier, "")
	},
}

var Account = bootstrap.Account{Name: names.Names.EnvSourceSyncAccount, Purpose: Purpose}
