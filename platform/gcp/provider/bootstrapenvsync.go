package gcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
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
	envSyncImageName   = "ocel-envsync"
	envSyncImagePath   = "/envsync"
	envSyncCPU         = "0.08"
	envSyncMemory      = "128Mi"
	envSyncGeneration  = "EXECUTION_ENVIRONMENT_GEN1"
	envSyncConcurrency = 1
	envSyncInstances   = 1
	envSyncTimeout     = "60s"
	envSyncIngress     = "INGRESS_TRAFFIC_INTERNAL_ONLY"
	envSyncTagLength   = 32

	envSyncRecordsRole = "roles/datastore.user"
	envSyncSealingRole = "roles/cloudkms.cryptoKeyEncrypter"
	envSyncOpeningRole = "roles/cloudkms.cryptoKeyDecrypter"
	envSyncCallRole    = "roles/run.invoker"

	envSyncSchedule          = "* * * * *"
	envSyncRequestsPerHour   = 60
	envSyncBilledSecondsEach = 2
	envSyncTimeZone          = "Etc/UTC"
	envSyncCall              = "POST"

	reasonUnsynced = "it stands, and it may not write this project's records or seal under the class key, so the syncer it runs as would write nothing"
	reasonStale    = "it runs another syncer than the one this provider carries, runs it as another account, or is billed, scaled or reached otherwise than this bootstrap stands it"
	reasonUncalled = "it stands, and the account Cloud Scheduler calls it as may not call it, or anyone may"
	reasonResched  = "it calls the syncer on another schedule, at another address or as another account than this bootstrap names"
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
			description: "the identity Cloud Scheduler calls the ocel env syncer of the " + string(class) + " class as",
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

func (b bootstrapper) envSyncService(class providerkit.Class) *run.GoogleCloudRunV2Service {
	return &run.GoogleCloudRunV2Service{
		Ingress:            envSyncIngress,
		InvokerIamDisabled: false,
		Template: &run.GoogleCloudRunV2RevisionTemplate{
			Containers: []*run.GoogleCloudRunV2Container{{
				Image: envSyncRef(b.clients, class),
				Ports: []*run.GoogleCloudRunV2ContainerPort{{ContainerPort: providerkit.InjectedPort}},
				Env: environmentOf(map[string]string{
					providerkit.NamespaceEnvVar: string(b.clients.Namespace()),
					ports.ProjectEnvVar:         b.clients.project,
					ports.RegionEnvVar:          b.clients.region,
					ports.ClassEnvVar:           string(class),
				}),
				Resources: &run.GoogleCloudRunV2ResourceRequirements{
					CpuIdle:         true,
					Limits:          map[string]string{"cpu": envSyncCPU, "memory": envSyncMemory},
					ForceSendFields: []string{"CpuIdle"},
				},
			}},
			Scaling: &run.GoogleCloudRunV2RevisionScaling{
				MinInstanceCount: 0,
				MaxInstanceCount: envSyncInstances,
				ForceSendFields:  []string{"MinInstanceCount"},
			},
			MaxInstanceRequestConcurrency: envSyncConcurrency,
			ExecutionEnvironment:          envSyncGeneration,
			ServiceAccount:                b.clients.EnvSyncAccountEmail(class),
			Timeout:                       envSyncTimeout,
		},
		ForceSendFields: []string{"InvokerIamDisabled"},
	}
}

func sameService(held, want *run.GoogleCloudRunV2Service) bool {
	if held.Template == nil || len(held.Template.Containers) != 1 || held.Template.Containers[0].Resources == nil {
		return false
	}
	task, wanted := held.Template, want.Template
	container, desired := task.Containers[0], wanted.Containers[0]
	scaling := task.Scaling
	if scaling == nil {
		scaling = &run.GoogleCloudRunV2RevisionScaling{}
	}
	return container.Image == desired.Image &&
		task.ServiceAccount == wanted.ServiceAccount &&
		held.Ingress == want.Ingress &&
		!held.InvokerIamDisabled &&
		container.Resources.CpuIdle &&
		maps.Equal(container.Resources.Limits, desired.Resources.Limits) &&
		scaling.MinInstanceCount == wanted.Scaling.MinInstanceCount &&
		scaling.MaxInstanceCount == wanted.Scaling.MaxInstanceCount &&
		slices.EqualFunc(container.Env, desired.Env, func(a, b *run.GoogleCloudRunV2EnvVar) bool {
			return a.Name == b.Name && a.Value == b.Value
		})
}

func (b bootstrapper) readService(ctx context.Context, name string) (*run.GoogleCloudRunV2Service, error) {
	services, err := b.clients.Run()
	if err != nil {
		return nil, err
	}
	held, err := attempted(ctx, func(call ...googleapi.CallOption) (*run.GoogleCloudRunV2Service, error) {
		return services.Projects.Locations.Services.Get(b.clients.servicePath(name)).Context(ctx).Do(call...)
	})
	if absent(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the Cloud Run service %s: %w", name, err)
	}
	return held, nil
}

func (b bootstrapper) serviceStands(ctx context.Context, class providerkit.Class, name string) (standing, error) {
	held, err := b.readService(ctx, name)
	if err != nil || held == nil {
		return standing{}, err
	}
	if !sameService(held, b.envSyncService(class)) {
		return standing{held: true, mends: reasonStale}, nil
	}
	called, err := b.callHeld(ctx, class, name)
	if err != nil {
		return standing{}, err
	}
	if !called {
		return standing{held: true, mends: reasonUncalled}, nil
	}
	return standing{held: true}, nil
}

func (b bootstrapper) makeService(ctx context.Context, class providerkit.Class, name string) error {
	if b.images == nil {
		return providerkit.Refuse(providerkit.CodeInvalid, "the %s service runs an image, and this bootstrap was opened with nowhere to push one", name)
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
	desired := b.envSyncService(class)
	path := b.clients.servicePath(name)
	held, err := b.readService(ctx, name)
	switch {
	case err != nil:
		return err
	case held == nil:
		err = runSettled(ctx, services, func(call ...googleapi.CallOption) (*run.GoogleLongrunningOperation, error) {
			return services.Projects.Locations.Services.Create(b.clients.location(), desired).ServiceId(name).Context(ctx).Do(call...)
		})
	default:
		desired.Etag = held.Etag
		err = runSettled(ctx, services, func(call ...googleapi.CallOption) (*run.GoogleLongrunningOperation, error) {
			return services.Projects.Locations.Services.Patch(path, desired).Context(ctx).Do(call...)
		})
	}
	if err != nil {
		return fmt.Errorf("stand the Cloud Run service %s: %w", name, err)
	}
	return b.letCall(ctx, class, name)
}

func (b bootstrapper) servicePolicy(ctx context.Context, name string) (*run.GoogleIamV1Policy, error) {
	services, err := b.clients.Run()
	if err != nil {
		return nil, err
	}
	policy, err := attempted(ctx, func(call ...googleapi.CallOption) (*run.GoogleIamV1Policy, error) {
		return services.Projects.Locations.Services.GetIamPolicy(b.clients.servicePath(name)).Context(ctx).Do(call...)
	})
	if err != nil {
		return nil, fmt.Errorf("read who may call the Cloud Run service %s: %w", name, err)
	}
	return policy, nil
}

func callMember(c *clients, class providerkit.Class) string {
	return "serviceAccount:" + c.EnvSyncInvokerEmail(class)
}

func (b bootstrapper) callHeld(ctx context.Context, class providerkit.Class, name string) (bool, error) {
	policy, err := b.servicePolicy(ctx, name)
	if err != nil {
		return false, err
	}
	_, changed := boundCaller(policy.Bindings, callMember(b.clients, class))
	return !changed, nil
}

func (b bootstrapper) letCall(ctx context.Context, class providerkit.Class, name string) error {
	services, err := b.clients.Run()
	if err != nil {
		return err
	}
	member := callMember(b.clients, class)
	var refused error
	for attempt := range bindAttempts {
		if attempt > 0 && !waited(ctx, attempt) {
			return ctx.Err()
		}
		policy, err := b.servicePolicy(ctx, name)
		if err != nil {
			return err
		}
		bindings, changed := boundCaller(policy.Bindings, member)
		if !changed {
			return nil
		}
		policy.Bindings = bindings
		_, refused = attempted(ctx, func(call ...googleapi.CallOption) (*run.GoogleIamV1Policy, error) {
			return services.Projects.Locations.Services.SetIamPolicy(b.clients.servicePath(name),
				&run.GoogleIamV1SetIamPolicyRequest{Policy: policy}).Context(ctx).Do(call...)
		})
		if refused == nil {
			return nil
		}
		if !taken(refused) {
			break
		}
	}
	return fmt.Errorf("let %s call the Cloud Run service %s: %w", member, name, refused)
}

var publicMembers = []string{"allUsers", "allAuthenticatedUsers"}

func boundCaller(bindings []*run.GoogleIamV1Binding, member string) ([]*run.GoogleIamV1Binding, bool) {
	for _, binding := range bindings {
		if binding.Role != envSyncCallRole {
			continue
		}
		kept := slices.DeleteFunc(slices.Clone(binding.Members), func(held string) bool { return slices.Contains(publicMembers, held) })
		members, added := boundMembers(kept, member, true)
		changed := added || len(kept) != len(binding.Members)
		binding.Members = members
		return bindings, changed
	}
	return append(bindings, &run.GoogleIamV1Binding{Role: envSyncCallRole, Members: []string{member}}), true
}

func (b bootstrapper) takeService(ctx context.Context, name string) error {
	services, err := b.clients.Run()
	if err != nil {
		return err
	}
	err = runSettled(ctx, services, func(call ...googleapi.CallOption) (*run.GoogleLongrunningOperation, error) {
		return services.Projects.Locations.Services.Delete(b.clients.servicePath(name)).Context(ctx).Do(call...)
	})
	if err != nil && !absent(err) {
		return fmt.Errorf("delete the Cloud Run service %s: %w", name, err)
	}
	return nil
}

func schedulePath(c *clients, name string) string { return c.location() + "/jobs/" + name }

func (b bootstrapper) envSyncSchedule(class providerkit.Class, name, uri string) *cloudscheduler.Job {
	return &cloudscheduler.Job{
		Name:        schedulePath(b.clients, name),
		Description: "calls the ocel env syncer of the " + string(class) + " class",
		Schedule:    envSyncSchedule,
		TimeZone:    envSyncTimeZone,
		HttpTarget: &cloudscheduler.HttpTarget{
			Uri:        uri + "/",
			HttpMethod: envSyncCall,
			OidcToken: &cloudscheduler.OidcToken{
				ServiceAccountEmail: b.clients.EnvSyncInvokerEmail(class),
				Audience:            uri,
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
	if token := held.HttpTarget.OidcToken; token != nil {
		signed = token.ServiceAccountEmail == want.HttpTarget.OidcToken.ServiceAccountEmail &&
			token.Audience == want.HttpTarget.OidcToken.Audience
	}
	return signed &&
		held.HttpTarget.OauthToken == nil &&
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
	held, err := attempted(ctx, service.Projects.Locations.Jobs.Get(schedulePath(b.clients, name)).Context(ctx).Do)
	if absent(err) {
		return standing{}, nil
	}
	if err != nil {
		return standing{}, fmt.Errorf("read the Cloud Scheduler job %s: %w", name, err)
	}
	called, err := b.readService(ctx, name)
	if err != nil {
		return standing{}, err
	}
	if called == nil || !sameSchedule(held, b.envSyncSchedule(class, name, called.Uri), b.clients.emulated()) {
		return standing{held: true, mends: reasonResched}, nil
	}
	return standing{held: true}, nil
}

func (b bootstrapper) makeSchedule(ctx context.Context, class providerkit.Class, name string) error {
	called, err := b.readService(ctx, name)
	if err != nil {
		return err
	}
	if called == nil || called.Uri == "" {
		return providerkit.Refuse(providerkit.CodeNotReady,
			"the Cloud Scheduler job %s calls the Cloud Run service of the same name, and that service publishes no url yet", name)
	}
	service, err := b.clients.Scheduler()
	if err != nil {
		return err
	}
	desired := b.envSyncSchedule(class, name, called.Uri)
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
	if _, err := attempted(ctx, service.Projects.Locations.Jobs.Delete(schedulePath(b.clients, name)).Context(ctx).Do); err != nil && !absent(err) {
		return fmt.Errorf("delete the Cloud Scheduler job %s: %w", name, err)
	}
	return nil
}
