package gcp

import (
	"context"
	"fmt"
	"maps"
	"math/big"
	"slices"
	"strings"
	"time"

	"google.golang.org/api/cloudscheduler/v1"
	"google.golang.org/api/googleapi"
	runv2 "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/policy"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
	"github.com/ocelhq/ocel/platform/gcp/provider/payloads"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
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
	scheduleTimeZone      = "Etc/UTC"
	syncScheduleMethod    = "POST"
	scheduleUpdateMask    = "description,schedule,timeZone,httpTarget,retryConfig"

	reasonUnwritable      = "it exists, and it may not write this project's records or seal under the tier key, so the env source sync running as it would write nothing"
	reasonServiceChanged  = "it runs another env source sync than the one this provider embeds, runs it as another account, or is billed, scaled, cut off or reached otherwise than this bootstrap deploys it"
	reasonCallersChanged  = "it exists, and the account Cloud Scheduler calls it as may not call it, or another may"
	reasonScheduleChanged = "it calls the env source sync on another schedule, at another URL or as another account than this bootstrap names"
	reasonScheduleStopped = "it is paused, disabled by Cloud Scheduler or left by a failed update, so it never calls the env source sync"

	scheduleEnabled  = "ENABLED"
	schedulePaused   = "PAUSED"
	scheduleDisabled = "DISABLED"
)

var syncKeyRoles = []string{syncSealingRole, syncOpeningRole}

var syncImageTag = binaryImageTag(payloads.EnvSourceSync)

func syncImageRef(c *gcloud.Clients, tier environment.Tier) string {
	return c.RepositoryPath(c.Region, tier) + "/" + syncImageName + ":" + syncImageTag()
}

func syncMember(c *gcloud.Clients, tier environment.Tier) string {
	return "serviceAccount:" + c.EnvSourceSyncAccountEmail(tier)
}

func (b bootstrap) syncPurpose(tier environment.Tier) accountPurpose {
	return accountPurpose{
		displayName: "ocel env source sync (" + b.clients.Namespace().String() + ", " + string(tier) + ")",
		description: "the identity the ocel env source sync of the " + string(tier) + " tier runs as, and the one Cloud Scheduler calls it as",
		ungranted:   reasonUnwritable,
		grant:       b.grantSyncWrites,
		forget:      b.forgetSyncWrites,
		granted:     b.syncWritesGranted,
	}
}

func (b bootstrap) grantSyncWrites(ctx context.Context, tier environment.Tier) error {
	member := syncMember(b.clients, tier)
	if err := gcloud.BindProjectRole(ctx, b.clients, member, syncRecordsRole, tierRecordsCondition(b.clients, tier), true); err != nil {
		return fmt.Errorf("let %s read and write the %s tier's records: %w", member, tier, err)
	}
	if _, err := gcloud.BindKeyRoles(ctx, b.clients, tier, member, syncKeyRoles, syncKeyRoles); err != nil {
		return fmt.Errorf("let %s seal and open values under the %s key: %w", member, tier, err)
	}
	if _, err := bindProjectKeyRoles(ctx, b.clients, tier, member, syncKeyRoles, syncKeyRoles); err != nil {
		return fmt.Errorf("let %s seal and open values under the %s tier's project keys: %w", member, tier, err)
	}
	return nil
}

func (b bootstrap) forgetSyncWrites(ctx context.Context, tier environment.Tier) error {
	member := syncMember(b.clients, tier)
	return everyStep(
		func() error {
			return gcloud.BindProjectRole(ctx, b.clients, member, syncRecordsRole, tierRecordsCondition(b.clients, tier), false)
		},
		func() error {
			_, err := gcloud.BindKeyRoles(ctx, b.clients, tier, member, syncKeyRoles, nil)
			return err
		},
		func() error {
			_, err := bindProjectKeyRoles(ctx, b.clients, tier, member, syncKeyRoles, nil)
			return err
		},
	)
}

func (b bootstrap) syncWritesGranted(ctx context.Context, tier environment.Tier) (bool, error) {
	member := syncMember(b.clients, tier)
	records, err := gcloud.ProjectRoleGranted(ctx, b.clients, member, syncRecordsRole, tierRecordsCondition(b.clients, tier))
	if err != nil || !records {
		return false, err
	}
	keyed, err := gcloud.KeyRolesGranted(ctx, b.clients, tier, member, syncKeyRoles)
	if err != nil || !keyed {
		return false, err
	}
	return projectKeyRolesGranted(ctx, b.clients, tier, member, syncKeyRoles)
}

func (b bootstrap) syncServing(tier environment.Tier) run.Serving {
	return run.Serving{
		Service:     b.clients.EnvSourceSync(tier),
		Image:       syncImageRef(b.clients, tier),
		Account:     b.clients.EnvSourceSyncAccountEmail(tier),
		Compute:     provider.ComputeServerless,
		CPU:         syncCPU,
		Memory:      syncMemoryMiB,
		Concurrency: syncConcurrency,
		Generation:  syncGeneration,
		Instances:   provider.Instances{Max: syncInstances},
		Timeout:     syncTimeout,
		Ingress:     run.IngressInternal,
		Env: map[string]string{
			provider.NamespaceEnvVar: string(b.clients.Namespace()),
			ports.ProjectEnvVar:      b.clients.Project(),
			ports.RegionEnvVar:       b.clients.Region,
			ports.TierEnvVar:         string(tier),
		},
	}
}

func sameService(current, desired *runv2.GoogleCloudRunV2Service) bool {
	if current.Template == nil || len(current.Template.Containers) != 1 || current.Template.Containers[0].Resources == nil {
		return false
	}
	template, wanted := current.Template, desired.Template
	container, want := template.Containers[0], wanted.Containers[0]
	scaling := template.Scaling
	if scaling == nil {
		scaling = &runv2.GoogleCloudRunV2RevisionScaling{}
	}
	return container.Image == want.Image &&
		template.ServiceAccount == wanted.ServiceAccount &&
		current.Ingress == desired.Ingress &&
		!current.InvokerIamDisabled &&
		container.Resources.CpuIdle &&
		sameLimits(container.Resources.Limits, want.Resources.Limits) &&
		scaling.MinInstanceCount == wanted.Scaling.MinInstanceCount &&
		scaling.MaxInstanceCount == wanted.Scaling.MaxInstanceCount &&
		template.MaxInstanceRequestConcurrency == wanted.MaxInstanceRequestConcurrency &&
		template.ExecutionEnvironment == wanted.ExecutionEnvironment &&
		sameTimeout(template.Timeout, wanted.Timeout) &&
		slices.EqualFunc(container.Env, want.Env, func(a, b *runv2.GoogleCloudRunV2EnvVar) bool {
			return a.Name == b.Name && a.Value == b.Value
		})
}

func sameTimeout(current, desired string) bool {
	got, err := time.ParseDuration(current)
	if err != nil {
		return false
	}
	want, err := time.ParseDuration(desired)
	return err == nil && got == want
}

func (b bootstrap) readService(ctx context.Context, name string) (*runv2.GoogleCloudRunV2Service, error) {
	services, err := b.clients.Run()
	if err != nil {
		return nil, err
	}
	found, err := googleretry.Attempted(ctx, func(call ...googleapi.CallOption) (*runv2.GoogleCloudRunV2Service, error) {
		return services.Projects.Locations.Services.Get(b.clients.ServicePath(name)).Context(ctx).Do(call...)
	})
	if gcloud.Absent(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the Cloud Run service %s: %w", name, err)
	}
	return found, nil
}

func (b bootstrap) servicePresence(ctx context.Context, tier environment.Tier, name string) (presence, error) {
	current, err := b.readService(ctx, name)
	if err != nil || current == nil {
		return presence{}, err
	}
	desired, err := run.ServiceOf(b.syncServing(tier))
	if err != nil {
		return presence{}, err
	}
	if !sameService(current, desired) || !run.ServesLatest(current) {
		return presence{present: true, mends: reasonServiceChanged}, nil
	}
	callable, err := b.onlySyncCalls(ctx, tier, name)
	if err != nil {
		return presence{}, err
	}
	if !callable {
		return presence{present: true, mends: reasonCallersChanged}, nil
	}
	return presence{present: true}, nil
}

func (b bootstrap) makeService(ctx context.Context, tier environment.Tier, name string) error {
	if b.pushBinary == nil || b.deployAndRoute == nil {
		return refusal.Refuse(refusal.CodeInvalid,
			"the %s service runs an image, and this bootstrap was opened with nowhere to push one or nothing to deploy it with", name)
	}
	if err := b.pushBinary(ctx, tier, syncImageName, syncImageRef(b.clients, tier), payloads.EnvSourceSync(), syncImagePath); err != nil {
		return err
	}
	if _, err := b.deployAndRoute(ctx, b.syncServing(tier), nil); err != nil {
		return err
	}
	return b.letSyncCall(ctx, tier, name)
}

func (b bootstrap) takeService(ctx context.Context, name string) error {
	if b.tearDown == nil {
		return refusal.Refuse(refusal.CodeInvalid, "this bootstrap was opened with nothing to take the %s service down with", name)
	}
	return b.tearDown(ctx, name, nil)
}

func (b bootstrap) onlySyncCalls(ctx context.Context, tier environment.Tier, name string) (bool, error) {
	bindings, err := policy.Read(ctx, gcloud.RunServicePolicy(b.clients, name))
	if err != nil {
		return false, err
	}
	_, changed := policy.OnlyMember(syncInvokerRole, syncMember(b.clients, tier))(bindings)
	return !changed, nil
}

func (b bootstrap) letSyncCall(ctx context.Context, tier environment.Tier, name string) error {
	_, err := policy.Edit(ctx, gcloud.RunServicePolicy(b.clients, name), policy.OnlyMember(syncInvokerRole, syncMember(b.clients, tier)))
	return err
}

func schedulePath(c *gcloud.Clients, name string) string { return c.Location() + "/jobs/" + name }

func (b bootstrap) syncSchedule(tier environment.Tier, name, uri string) *cloudscheduler.Job {
	return &cloudscheduler.Job{
		Name:        schedulePath(b.clients, name),
		Description: "calls the ocel env source sync of the " + string(tier) + " tier",
		Schedule:    syncSchedule,
		TimeZone:    scheduleTimeZone,
		HttpTarget: &cloudscheduler.HttpTarget{
			Uri:        uri + "/",
			HttpMethod: syncScheduleMethod,
			OidcToken: &cloudscheduler.OidcToken{
				ServiceAccountEmail: b.clients.EnvSourceSyncAccountEmail(tier),
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

func (b bootstrap) schedulePresence(ctx context.Context, tier environment.Tier, name string) (presence, error) {
	service, err := b.clients.Scheduler()
	if err != nil {
		return presence{}, err
	}
	current, err := googleretry.Attempted(ctx, service.Projects.Locations.Jobs.Get(schedulePath(b.clients, name)).Context(ctx).Do)
	if gcloud.Absent(err) {
		return presence{}, nil
	}
	if err != nil {
		return presence{}, fmt.Errorf("read the Cloud Scheduler job %s: %w", name, err)
	}
	called, err := b.readService(ctx, name)
	if err != nil {
		return presence{}, err
	}
	if called == nil || !sameSchedule(current, b.syncSchedule(tier, name, called.Uri), b.clients.Emulated()) {
		return presence{present: true, mends: reasonScheduleChanged}, nil
	}
	if current.State != scheduleEnabled {
		return presence{present: true, mends: reasonScheduleStopped}, nil
	}
	return presence{present: true}, nil
}

func (b bootstrap) makeSchedule(ctx context.Context, tier environment.Tier, name string) error {
	called, err := b.readService(ctx, name)
	if err != nil {
		return err
	}
	if called == nil || called.Uri == "" {
		return refusal.Refuse(refusal.CodeNotReady,
			"the Cloud Scheduler job %s calls the Cloud Run service of the same name, and that service has no URL yet", name)
	}
	return putSchedule(ctx, b.clients, name, b.syncSchedule(tier, name, called.Uri))
}

func putSchedule(ctx context.Context, c *gcloud.Clients, name string, desired *cloudscheduler.Job) error {
	service, err := c.Scheduler()
	if err != nil {
		return err
	}
	path := schedulePath(c, name)
	create := func() (*cloudscheduler.Job, error) {
		return googleretry.Attempted(ctx, service.Projects.Locations.Jobs.Create(c.Location(), desired).Context(ctx).Do)
	}
	job, err := create()
	if gcloud.Taken(err) {
		patched := *desired
		patched.Name = ""
		job, err = googleretry.Attempted(ctx, service.Projects.Locations.Jobs.Patch(path, &patched).
			UpdateMask(scheduleUpdateMask).Context(ctx).Do)
	}
	if err != nil {
		return fmt.Errorf("create the Cloud Scheduler job %s: %w", name, err)
	}
	switch job.State {
	case schedulePaused:
		if _, err := googleretry.Attempted(ctx, service.Projects.Locations.Jobs.Resume(path, &cloudscheduler.ResumeJobRequest{}).Context(ctx).Do); err != nil {
			return fmt.Errorf("resume the paused Cloud Scheduler job %s: %w", name, err)
		}
	case scheduleDisabled:
		if err := takeSchedule(ctx, c, name); err != nil {
			return err
		}
		if _, err := create(); err != nil {
			return fmt.Errorf("create the Cloud Scheduler job %s again in place of the one Cloud Scheduler disabled: %w", name, err)
		}
	}
	return nil
}

func takeSchedule(ctx context.Context, c *gcloud.Clients, name string) error {
	service, err := c.Scheduler()
	if err != nil {
		return err
	}
	if _, err := googleretry.Attempted(ctx, service.Projects.Locations.Jobs.Delete(schedulePath(c, name)).Context(ctx).Do); err != nil && !gcloud.Absent(err) {
		return fmt.Errorf("delete the Cloud Scheduler job %s: %w", name, err)
	}
	return nil
}

var quantitySuffixes = []struct {
	suffix string
	scale  *big.Rat
}{
	{"Ki", new(big.Rat).SetInt64(1 << 10)},
	{"Mi", new(big.Rat).SetInt64(1 << 20)},
	{"Gi", new(big.Rat).SetInt64(1 << 30)},
	{"Ti", new(big.Rat).SetInt64(1 << 40)},
	{"m", big.NewRat(1, 1000)},
	{"k", new(big.Rat).SetInt64(1e3)},
	{"M", new(big.Rat).SetInt64(1e6)},
	{"G", new(big.Rat).SetInt64(1e9)},
	{"T", new(big.Rat).SetInt64(1e12)},
}

func quantity(written string) (*big.Rat, bool) {
	scale := big.NewRat(1, 1)
	for _, each := range quantitySuffixes {
		if number, cut := strings.CutSuffix(written, each.suffix); cut {
			written, scale = number, each.scale
			break
		}
	}
	amount, ok := new(big.Rat).SetString(written)
	if !ok {
		return nil, false
	}
	return amount.Mul(amount, scale), true
}

func sameLimits(current, desired map[string]string) bool {
	return maps.EqualFunc(current, desired, func(a, b string) bool {
		got, readable := quantity(a)
		want, wanted := quantity(b)
		return readable && wanted && got.Cmp(want) == 0
	})
}
