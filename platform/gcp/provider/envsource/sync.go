package envsource

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/api/cloudscheduler/v1"
	"google.golang.org/api/googleapi"
	runv2 "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/policy"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
	"github.com/ocelhq/ocel/platform/gcp/provider/payloads"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/variables"
)

const (
	syncImageName   = "ocel-envsourcesync"
	syncImagePath   = "/envsourcesync"
	syncCPU         = "0.08"
	syncMemoryMiB   = 128
	syncGeneration  = "EXECUTION_ENVIRONMENT_GEN1"
	syncConcurrency = 1
	syncInstances   = 1
	syncTimeout     = 60 * time.Second

	syncRecordsRole = "roles/datastore.user"
	syncSealingRole = "roles/cloudkms.cryptoKeyEncrypter"
	syncOpeningRole = "roles/cloudkms.cryptoKeyDecrypter"
	syncInvokerRole = "roles/run.invoker"

	syncSchedule          = "* * * * *"
	syncRequestsPerHour   = 60
	syncBilledSecondsEach = 2
	syncScheduleMethod    = "POST"

	reasonUnwritable      = "it may not write this project's records or seal under the tier key"
	reasonServiceChanged  = "it drifted from the env source sync this build deploys"
	reasonCallersChanged  = "who may call it drifted"
	reasonScheduleChanged = "its schedule, URL or caller drifted"
	reasonScheduleStopped = "it is paused, disabled or left by a failed update"
)

var SyncKeyRoles = []string{syncSealingRole, syncOpeningRole}

var syncImageTag = run.BinaryImageTag(payloads.EnvSourceSync)

func syncImageRef(c *gcloud.Clients, tier environment.Tier) string {
	return c.RepositoryPath(c.Region, tier) + "/" + syncImageName + ":" + syncImageTag()
}

func syncMember(c *gcloud.Clients, tier environment.Tier) string {
	return "serviceAccount:" + c.EnvSourceSyncAccountEmail(tier)
}

func Purpose(b bootstrap.Engine, tier environment.Tier) bootstrap.AccountPurpose {
	return bootstrap.AccountPurpose{
		DisplayName: "ocel env source sync (" + b.Clients.Namespace().String() + ", " + string(tier) + ")",
		Description: "the identity the ocel env source sync of the " + string(tier) + " tier runs as, and the one Cloud Scheduler calls it as",
		Ungranted:   reasonUnwritable,
		Grant:       grantSyncWrites,
		Forget:      forgetSyncWrites,
		Granted:     syncWritesGranted,
	}
}

func grantSyncWrites(ctx context.Context, b bootstrap.Engine, tier environment.Tier) error {
	member := syncMember(b.Clients, tier)
	if err := gcloud.BindProjectRole(ctx, b.Clients, member, syncRecordsRole, variables.RecordsCondition(b.Clients, tier), true); err != nil {
		return fmt.Errorf("let %s read and write the %s tier's records: %w", member, tier, err)
	}
	if _, err := gcloud.BindKeyRoles(ctx, b.Clients, tier, member, SyncKeyRoles, SyncKeyRoles); err != nil {
		return fmt.Errorf("let %s seal and open values under the %s key: %w", member, tier, err)
	}
	return nil
}

func forgetSyncWrites(ctx context.Context, b bootstrap.Engine, tier environment.Tier) error {
	member := syncMember(b.Clients, tier)
	return gcloud.EveryStep(
		func() error {
			return gcloud.BindProjectRole(ctx, b.Clients, member, syncRecordsRole, variables.RecordsCondition(b.Clients, tier), false)
		},
		func() error {
			on, err := bootstrap.Keys.HasAPIsOn(ctx, b)
			if err != nil || !on {
				return err
			}
			_, err = gcloud.BindKeyRoles(ctx, b.Clients, tier, member, SyncKeyRoles, nil)
			return err
		},
	)
}

func syncWritesGranted(ctx context.Context, b bootstrap.Engine, tier environment.Tier) (bool, error) {
	member := syncMember(b.Clients, tier)
	records, err := gcloud.ProjectRoleGranted(ctx, b.Clients, member, syncRecordsRole, variables.RecordsCondition(b.Clients, tier))
	if err != nil || !records {
		return false, err
	}
	return gcloud.KeyRolesGranted(ctx, b.Clients, tier, member, SyncKeyRoles)
}

func letSyncSealFor(ctx context.Context, b bootstrap.Engine, tier environment.Tier, project string) error {
	if err := variables.EnsureProjectKey(ctx, b.Clients, tier, project); err != nil {
		return err
	}
	key := b.Clients.Workload().ProjectKeyPath(tier, project)
	member := syncMember(b.Clients, tier)
	changes := make([]policy.Change, 0, len(SyncKeyRoles))
	for _, role := range SyncKeyRoles {
		changes = append(changes, policy.Grant(role, member, nil))
	}
	if _, err := policy.Edit(ctx, gcloud.KeyPolicy(b.Clients, key, "project "+project+"'s "+string(tier)), policy.Each(changes...)); err != nil {
		return fmt.Errorf("let %s seal and open project %s's %s values: %w", member, project, tier, err)
	}
	return nil
}

func SyncServing(b bootstrap.Engine, tier environment.Tier) run.Serving {
	return run.Serving{
		Service:     b.Clients.EnvSourceSync(tier),
		Tier:        tier,
		Image:       syncImageRef(b.Clients, tier),
		Account:     b.Clients.EnvSourceSyncAccountEmail(tier),
		Compute:     provider.ComputeServerless,
		CPU:         syncCPU,
		Memory:      syncMemoryMiB,
		Concurrency: syncConcurrency,
		Generation:  syncGeneration,
		Instances:   provider.Instances{Max: syncInstances},
		Timeout:     syncTimeout,
		Ingress:     run.IngressInternal,
		Env: map[string]string{
			provider.NamespaceEnvVar: string(b.Clients.Namespace()),
			ports.ProjectEnvVar:      b.Clients.Project(),
			ports.RegionEnvVar:       b.Clients.Region,
			ports.TierEnvVar:         string(tier),
		},
	}
}

func readService(ctx context.Context, b bootstrap.Engine, name string) (*runv2.GoogleCloudRunV2Service, error) {
	services, err := b.Clients.Run()
	if err != nil {
		return nil, err
	}
	found, err := googleretry.Attempted(ctx, func(call ...googleapi.CallOption) (*runv2.GoogleCloudRunV2Service, error) {
		return services.Projects.Locations.Services.Get(b.Clients.ServicePath(name)).Context(ctx).Do(call...)
	})
	if gcloud.Absent(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the Cloud Run service %s: %w", name, err)
	}
	return found, nil
}

func servicePresence(ctx context.Context, b bootstrap.Engine, tier environment.Tier, name string) (bootstrap.Presence, error) {
	current, err := readService(ctx, b, name)
	if err != nil || current == nil {
		return bootstrap.Presence{}, err
	}
	desired, err := run.ServiceOf(SyncServing(b, tier))
	if err != nil {
		return bootstrap.Presence{}, err
	}
	if run.Drifted(current, desired) || !run.ServesLatest(current) {
		return bootstrap.Presence{Present: true, Mends: reasonServiceChanged}, nil
	}
	callable, err := onlySyncCalls(ctx, b, tier, name)
	if err != nil {
		return bootstrap.Presence{}, err
	}
	if !callable {
		return bootstrap.Presence{Present: true, Mends: reasonCallersChanged}, nil
	}
	return bootstrap.Presence{Present: true}, nil
}

func makeService(ctx context.Context, b bootstrap.Engine, tier environment.Tier, name string) error {
	if b.PushBinary == nil || b.DeployAndRoute == nil {
		return refusal.Refuse(refusal.CodeInvalid,
			"the %s service runs an image, and this bootstrap was opened with nowhere to push one or nothing to deploy it with", name)
	}
	if err := b.PushBinary(ctx, tier, syncImageName, syncImageRef(b.Clients, tier), payloads.EnvSourceSync(), syncImagePath); err != nil {
		return err
	}
	if _, err := b.DeployAndRoute(ctx, SyncServing(b, tier), nil); err != nil {
		return err
	}
	return letSyncCall(ctx, b, tier, name)
}

func takeService(ctx context.Context, b bootstrap.Engine, name string) error {
	if b.TearDown == nil {
		return refusal.Refuse(refusal.CodeInvalid, "this bootstrap was opened with nothing to take the %s service down with", name)
	}
	return b.TearDown(ctx, name, nil)
}

func onlySyncCalls(ctx context.Context, b bootstrap.Engine, tier environment.Tier, name string) (bool, error) {
	bindings, err := policy.Read(ctx, gcloud.RunServicePolicy(b.Clients, name))
	if err != nil {
		return false, err
	}
	_, changed := policy.OnlyMember(syncInvokerRole, syncMember(b.Clients, tier))(bindings)
	return !changed, nil
}

func letSyncCall(ctx context.Context, b bootstrap.Engine, tier environment.Tier, name string) error {
	_, err := policy.Edit(ctx, gcloud.RunServicePolicy(b.Clients, name), policy.OnlyMember(syncInvokerRole, syncMember(b.Clients, tier)))
	return err
}

func syncJob(b bootstrap.Engine, tier environment.Tier, name, uri string) *cloudscheduler.Job {
	return &cloudscheduler.Job{
		Name:        gcloud.SchedulePath(b.Clients, name),
		Description: "calls the ocel env source sync of the " + string(tier) + " tier",
		Schedule:    syncSchedule,
		TimeZone:    gcloud.ScheduleTimeZone,
		HttpTarget: &cloudscheduler.HttpTarget{
			Uri:        uri + "/",
			HttpMethod: syncScheduleMethod,
			OidcToken: &cloudscheduler.OidcToken{
				ServiceAccountEmail: b.Clients.EnvSourceSyncAccountEmail(tier),
				Audience:            uri,
			},
		},
		RetryConfig: &cloudscheduler.RetryConfig{RetryCount: 0, ForceSendFields: []string{"RetryCount"}},
	}
}

func sameSchedule(current, desired *cloudscheduler.Job, emulated bool) bool {
	if current.HttpTarget == nil {
		return false
	}
	signed := emulated
	if token := current.HttpTarget.OidcToken; token != nil {
		signed = token.ServiceAccountEmail == desired.HttpTarget.OidcToken.ServiceAccountEmail &&
			token.Audience == desired.HttpTarget.OidcToken.Audience
	}
	return signed &&
		current.HttpTarget.OauthToken == nil &&
		current.Schedule == desired.Schedule &&
		current.TimeZone == desired.TimeZone &&
		current.HttpTarget.Uri == desired.HttpTarget.Uri &&
		current.HttpTarget.HttpMethod == desired.HttpTarget.HttpMethod &&
		(current.RetryConfig == nil || current.RetryConfig.RetryCount == 0)
}

func readSchedule(ctx context.Context, b bootstrap.Engine, name string) (*cloudscheduler.Job, error) {
	service, err := b.Clients.Scheduler()
	if err != nil {
		return nil, err
	}
	current, err := googleretry.Attempted(ctx, service.Projects.Locations.Jobs.Get(gcloud.SchedulePath(b.Clients, name)).Context(ctx).Do)
	if gcloud.Absent(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the Cloud Scheduler job %s: %w", name, err)
	}
	return current, nil
}

func schedulePresence(ctx context.Context, b bootstrap.Engine, tier environment.Tier, name string) (bootstrap.Presence, error) {
	current, err := readSchedule(ctx, b, name)
	if err != nil || current == nil {
		return bootstrap.Presence{}, err
	}
	called, err := readService(ctx, b, name)
	if err != nil {
		return bootstrap.Presence{}, err
	}
	if called == nil || !sameSchedule(current, syncJob(b, tier, name, called.Uri), b.Clients.Emulated()) {
		return bootstrap.Presence{Present: true, Mends: reasonScheduleChanged}, nil
	}
	if current.State != gcloud.ScheduleEnabled {
		return bootstrap.Presence{Present: true, Mends: reasonScheduleStopped}, nil
	}
	return bootstrap.Presence{Present: true}, nil
}

func makeSchedule(ctx context.Context, b bootstrap.Engine, tier environment.Tier, name string) error {
	called, err := readService(ctx, b, name)
	if err != nil {
		return err
	}
	if called == nil || called.Uri == "" {
		return refusal.Refuse(refusal.CodeNotReady,
			"the Cloud Scheduler job %s calls the Cloud Run service of the same name, and that service has no URL yet", name)
	}
	return gcloud.PutSchedule(ctx, b.Clients, name, syncJob(b, tier, name, called.Uri))
}
