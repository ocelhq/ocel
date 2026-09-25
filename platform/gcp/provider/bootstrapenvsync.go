package gcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"sync"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"google.golang.org/api/cloudscheduler/v1"
	"google.golang.org/api/googleapi"
	run "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/gcp/provider/payloads"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

const (
	envSyncImageName = "ocel-envsync"
	envSyncImagePath = "/envsync"
	envSyncCPU       = "1"
	envSyncMemory    = "512Mi"
	envSyncTimeout   = "60s"
	envSyncTagLength = 32

	envSyncRecordsRole = "roles/datastore.user"
	envSyncSealingRole = "roles/cloudkms.cryptoKeyEncrypter"
	envSyncOpeningRole = "roles/cloudkms.cryptoKeyDecrypter"
	envSyncStartRole   = "roles/run.invoker"

	envSyncSchedule          = "* * * * *"
	envSyncExecutionsPerHour = 60
	envSyncBilledSeconds     = 60
	envSyncTimeZone          = "Etc/UTC"
	envSyncStart             = "POST"
	runAPI                   = "https://run.googleapis.com"

	reasonUnsynced = "it stands, and it may not write this project's records or seal under the class key, so the syncer it runs as would write nothing"
	reasonStale    = "it runs another syncer than the one this provider carries, or runs it as another account"
	reasonUnstart  = "it stands, and the account Cloud Scheduler starts it as may not start it"
	reasonResched  = "it starts the syncer on another schedule, at another address or as another account than this bootstrap names"
)

var envSyncKeyRoles = []string{envSyncSealingRole, envSyncOpeningRole}

type imageStore interface {
	based(ctx context.Context, ref string) (v1.Image, error)
	pushImage(ctx context.Context, class providerkit.Class, app, ref string, built v1.Image, report providerkit.Reporter) error
}

var envSyncTag = sync.OnceValue(func() string {
	sum := sha256.New()
	sum.Write([]byte(staticImage + "\x00"))
	sum.Write(payloads.EnvSync())
	return hex.EncodeToString(sum.Sum(nil))[:envSyncTagLength]
})

func envSyncRef(c *clients, class providerkit.Class) string {
	return c.RepositoryPath(c.region, class) + "/" + envSyncImageName + ":" + envSyncTag()
}

func jobPath(c *clients, name string) string { return c.location() + "/jobs/" + name }

func (c *clients) runURL() string {
	if c.emulated() {
		return c.endpoint
	}
	return runAPI
}

type accountRole struct {
	display     string
	description string
	unheld      string
	grant       func(b bootstrapper, ctx context.Context, class providerkit.Class, granting bool) error
	held        func(b bootstrapper, ctx context.Context, class providerkit.Class) (bool, error)
}

func (b bootstrapper) roleOf(class providerkit.Class, name string) accountRole {
	switch name {
	case b.clients.EnvSyncAccount(class):
		return accountRole{
			display:     "ocel " + string(class) + " env syncer",
			description: "the identity the ocel env syncer of the " + string(class) + " class runs as",
			unheld:      reasonUnsynced,
			grant:       bootstrapper.grantSyncWrites,
			held:        bootstrapper.syncWritesHeld,
		}
	case b.clients.EnvSyncInvoker(class):
		return accountRole{
			display:     "ocel " + string(class) + " env syncer schedule",
			description: "the identity Cloud Scheduler starts the ocel env syncer of the " + string(class) + " class as",
		}
	default:
		return accountRole{
			display:     "ocel " + string(class) + " apps",
			description: "the identity every app ocel deploys in the " + string(class) + " class runs as",
			unheld:      reasonUnread,
			grant:       bootstrapper.holdReads,
			held:        bootstrapper.readsHeld,
		}
	}
}

func (b bootstrapper) holdReads(ctx context.Context, class providerkit.Class, granting bool) error {
	if granting {
		return b.grantReads(ctx, class)
	}
	return b.forgetReads(ctx, class)
}

func (b bootstrapper) grantSyncWrites(ctx context.Context, class providerkit.Class, granting bool) error {
	member := "serviceAccount:" + b.clients.EnvSyncAccountEmail(class)
	condition := databaseCondition(b.clients.project, b.clients.Namespace())
	wanted := []string(nil)
	if granting {
		wanted = envSyncKeyRoles
	}
	return everyStep(
		func() error {
			if err := b.clients.bindProjectRole(ctx, member, envSyncRecordsRole, condition, granting); err != nil {
				return fmt.Errorf("hold %s to reading and writing the %s database's records: %w", member, ports.Database(b.clients.Namespace()), err)
			}
			return nil
		},
		func() error {
			if _, err := b.clients.bindKeyRoles(ctx, class, member, envSyncKeyRoles, wanted); err != nil {
				return fmt.Errorf("hold %s to sealing and opening values under the %s key: %w", member, class, err)
			}
			return nil
		},
	)
}

func (b bootstrapper) syncWritesHeld(ctx context.Context, class providerkit.Class) (bool, error) {
	member := "serviceAccount:" + b.clients.EnvSyncAccountEmail(class)
	records, err := b.clients.projectRoleHeld(ctx, member, envSyncRecordsRole, databaseCondition(b.clients.project, b.clients.Namespace()))
	if err != nil || !records {
		return false, err
	}
	return b.clients.keyRolesHeld(ctx, class, member, envSyncKeyRoles)
}

func (b bootstrapper) envSyncJob(class providerkit.Class) *run.GoogleCloudRunV2Job {
	return &run.GoogleCloudRunV2Job{
		Template: &run.GoogleCloudRunV2ExecutionTemplate{
			TaskCount:   1,
			Parallelism: 1,
			Template: &run.GoogleCloudRunV2TaskTemplate{
				Containers: []*run.GoogleCloudRunV2Container{{
					Image: envSyncRef(b.clients, class),
					Env: environmentOf(map[string]string{
						providerkit.NamespaceEnvVar: string(b.clients.Namespace()),
						ports.ProjectEnvVar:         b.clients.project,
						ports.RegionEnvVar:          b.clients.region,
						ports.ClassEnvVar:           string(class),
					}),
					Resources: &run.GoogleCloudRunV2ResourceRequirements{
						Limits: map[string]string{"cpu": envSyncCPU, "memory": envSyncMemory},
					},
				}},
				ServiceAccount:  b.clients.EnvSyncAccountEmail(class),
				MaxRetries:      0,
				Timeout:         envSyncTimeout,
				ForceSendFields: []string{"MaxRetries"},
			},
		},
	}
}

func sameJob(held, want *run.GoogleCloudRunV2Job) bool {
	if held.Template == nil || held.Template.Template == nil || len(held.Template.Template.Containers) != 1 {
		return false
	}
	task, wanted := held.Template.Template, want.Template.Template
	container, desired := task.Containers[0], wanted.Containers[0]
	return container.Image == desired.Image &&
		task.ServiceAccount == wanted.ServiceAccount &&
		task.MaxRetries == wanted.MaxRetries &&
		slices.EqualFunc(container.Env, desired.Env, func(a, b *run.GoogleCloudRunV2EnvVar) bool {
			return a.Name == b.Name && a.Value == b.Value
		})
}

func (b bootstrapper) jobStands(ctx context.Context, class providerkit.Class, name string) (standing, error) {
	services, err := b.clients.Run()
	if err != nil {
		return standing{}, err
	}
	held, err := attempted(ctx, func(call ...googleapi.CallOption) (*run.GoogleCloudRunV2Job, error) {
		return services.Projects.Locations.Jobs.Get(jobPath(b.clients, name)).Context(ctx).Do(call...)
	})
	if absent(err) {
		return standing{}, nil
	}
	if err != nil {
		return standing{}, fmt.Errorf("read the Cloud Run job %s: %w", name, err)
	}
	if !sameJob(held, b.envSyncJob(class)) {
		return standing{held: true, mends: reasonStale}, nil
	}
	started, err := b.startHeld(ctx, class, name)
	if err != nil {
		return standing{}, err
	}
	if !started {
		return standing{held: true, mends: reasonUnstart}, nil
	}
	return standing{held: true}, nil
}

func (b bootstrapper) makeJob(ctx context.Context, class providerkit.Class, name string) error {
	if b.images == nil {
		return providerkit.Refuse(providerkit.CodeInvalid, "the %s job runs an image, and this bootstrap was opened with nowhere to push one", name)
	}
	base, err := b.images.based(ctx, staticImage)
	if err != nil {
		return err
	}
	built, err := binaryImage(base, payloads.EnvSync(), envSyncImagePath)
	if err != nil {
		return err
	}
	if err := b.images.pushImage(ctx, class, envSyncImageName, envSyncRef(b.clients, class), built, nil); err != nil {
		return err
	}
	services, err := b.clients.Run()
	if err != nil {
		return err
	}
	desired := b.envSyncJob(class)
	path := jobPath(b.clients, name)
	held, err := attempted(ctx, func(call ...googleapi.CallOption) (*run.GoogleCloudRunV2Job, error) {
		return services.Projects.Locations.Jobs.Get(path).Context(ctx).Do(call...)
	})
	switch {
	case absent(err):
		err = runSettled(ctx, services, func(call ...googleapi.CallOption) (*run.GoogleLongrunningOperation, error) {
			return services.Projects.Locations.Jobs.Create(b.clients.location(), desired).JobId(name).Context(ctx).Do(call...)
		})
	case err != nil:
		return fmt.Errorf("read the Cloud Run job %s: %w", name, err)
	default:
		desired.Etag = held.Etag
		err = runSettled(ctx, services, func(call ...googleapi.CallOption) (*run.GoogleLongrunningOperation, error) {
			return services.Projects.Locations.Jobs.Patch(path, desired).Context(ctx).Do(call...)
		})
	}
	if err != nil {
		return fmt.Errorf("stand the Cloud Run job %s: %w", name, err)
	}
	return b.letStart(ctx, class, name)
}

func (b bootstrapper) jobPolicy(ctx context.Context, name string) (*run.GoogleIamV1Policy, error) {
	services, err := b.clients.Run()
	if err != nil {
		return nil, err
	}
	policy, err := attempted(ctx, func(call ...googleapi.CallOption) (*run.GoogleIamV1Policy, error) {
		return services.Projects.Locations.Jobs.GetIamPolicy(jobPath(b.clients, name)).Context(ctx).Do(call...)
	})
	if err != nil {
		return nil, fmt.Errorf("read who may start the Cloud Run job %s: %w", name, err)
	}
	return policy, nil
}

func startMember(c *clients, class providerkit.Class) string {
	return "serviceAccount:" + c.EnvSyncInvokerEmail(class)
}

func (b bootstrapper) startHeld(ctx context.Context, class providerkit.Class, name string) (bool, error) {
	policy, err := b.jobPolicy(ctx, name)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(policy.Bindings, func(binding *run.GoogleIamV1Binding) bool {
		return binding.Role == envSyncStartRole && slices.Contains(binding.Members, startMember(b.clients, class))
	}), nil
}

func (b bootstrapper) letStart(ctx context.Context, class providerkit.Class, name string) error {
	services, err := b.clients.Run()
	if err != nil {
		return err
	}
	member := startMember(b.clients, class)
	var refused error
	for attempt := range bindAttempts {
		if attempt > 0 && !waited(ctx, attempt) {
			return ctx.Err()
		}
		policy, err := b.jobPolicy(ctx, name)
		if err != nil {
			return err
		}
		bindings, changed := boundJobMember(policy.Bindings, member)
		if !changed {
			return nil
		}
		policy.Bindings = bindings
		_, refused = attempted(ctx, func(call ...googleapi.CallOption) (*run.GoogleIamV1Policy, error) {
			return services.Projects.Locations.Jobs.SetIamPolicy(jobPath(b.clients, name),
				&run.GoogleIamV1SetIamPolicyRequest{Policy: policy}).Context(ctx).Do(call...)
		})
		if refused == nil {
			return nil
		}
		if !taken(refused) {
			break
		}
	}
	return fmt.Errorf("let %s start the Cloud Run job %s: %w", member, name, refused)
}

func boundJobMember(bindings []*run.GoogleIamV1Binding, member string) ([]*run.GoogleIamV1Binding, bool) {
	for _, binding := range bindings {
		if binding.Role != envSyncStartRole {
			continue
		}
		members, changed := boundMembers(binding.Members, member, true)
		binding.Members = members
		return bindings, changed
	}
	return append(bindings, &run.GoogleIamV1Binding{Role: envSyncStartRole, Members: []string{member}}), true
}

func (b bootstrapper) takeJob(ctx context.Context, name string) error {
	services, err := b.clients.Run()
	if err != nil {
		return err
	}
	err = runSettled(ctx, services, func(call ...googleapi.CallOption) (*run.GoogleLongrunningOperation, error) {
		return services.Projects.Locations.Jobs.Delete(jobPath(b.clients, name)).Context(ctx).Do(call...)
	})
	if err != nil && !absent(err) {
		return fmt.Errorf("delete the Cloud Run job %s: %w", name, err)
	}
	return nil
}

func (b bootstrapper) envSyncSchedule(class providerkit.Class, name string) *cloudscheduler.Job {
	return &cloudscheduler.Job{
		Name:        jobPath(b.clients, name),
		Description: "starts the ocel env syncer of the " + string(class) + " class",
		Schedule:    envSyncSchedule,
		TimeZone:    envSyncTimeZone,
		HttpTarget: &cloudscheduler.HttpTarget{
			Uri:        b.clients.runURL() + "/v2/" + jobPath(b.clients, name) + ":run",
			HttpMethod: envSyncStart,
			OauthToken: &cloudscheduler.OAuthToken{
				ServiceAccountEmail: b.clients.EnvSyncInvokerEmail(class),
				Scope:               ports.CloudPlatformScope,
			},
		},
		RetryConfig: &cloudscheduler.RetryConfig{RetryCount: 0, ForceSendFields: []string{"RetryCount"}},
	}
}

func sameSchedule(held, want *cloudscheduler.Job, emulated bool) bool {
	if held.HttpTarget == nil {
		return false
	}
	signed := emulated
	if token := held.HttpTarget.OauthToken; token != nil {
		signed = token.ServiceAccountEmail == want.HttpTarget.OauthToken.ServiceAccountEmail
	}
	return signed &&
		held.Schedule == want.Schedule &&
		held.TimeZone == want.TimeZone &&
		held.HttpTarget.Uri == want.HttpTarget.Uri &&
		held.HttpTarget.HttpMethod == want.HttpTarget.HttpMethod &&
		(held.RetryConfig == nil || held.RetryConfig.RetryCount == 0)
}

func (b bootstrapper) scheduleStands(ctx context.Context, class providerkit.Class, name string) (standing, error) {
	service, err := b.clients.Scheduler()
	if err != nil {
		return standing{}, err
	}
	held, err := attempted(ctx, service.Projects.Locations.Jobs.Get(jobPath(b.clients, name)).Context(ctx).Do)
	if absent(err) {
		return standing{}, nil
	}
	if err != nil {
		return standing{}, fmt.Errorf("read the Cloud Scheduler job %s: %w", name, err)
	}
	if !sameSchedule(held, b.envSyncSchedule(class, name), b.clients.emulated()) {
		return standing{held: true, mends: reasonResched}, nil
	}
	return standing{held: true}, nil
}

func (b bootstrapper) makeSchedule(ctx context.Context, class providerkit.Class, name string) error {
	service, err := b.clients.Scheduler()
	if err != nil {
		return err
	}
	desired := b.envSyncSchedule(class, name)
	_, err = attempted(ctx, service.Projects.Locations.Jobs.Create(b.clients.location(), desired).Context(ctx).Do)
	if taken(err) {
		path := desired.Name
		desired.Name = ""
		_, err = attempted(ctx, service.Projects.Locations.Jobs.Patch(path, desired).
			UpdateMask("description,schedule,timeZone,httpTarget,retryConfig").Context(ctx).Do)
	}
	if err != nil {
		return fmt.Errorf("stand the Cloud Scheduler job %s: %w", name, err)
	}
	return nil
}

func (b bootstrapper) takeSchedule(ctx context.Context, name string) error {
	service, err := b.clients.Scheduler()
	if err != nil {
		return err
	}
	if _, err := attempted(ctx, service.Projects.Locations.Jobs.Delete(jobPath(b.clients, name)).Context(ctx).Do); err != nil && !absent(err) {
		return fmt.Errorf("delete the Cloud Scheduler job %s: %w", name, err)
	}
	return nil
}
