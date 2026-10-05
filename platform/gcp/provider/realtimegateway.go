package gcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"sync"

	"google.golang.org/api/googleapi"
	run "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/payloads"
	"github.com/ocelhq/ocel/platform/realtime/gatewayenv"
)

const (
	realtimeGatewayCPU         = "1"
	realtimeGatewayMemoryMiB   = 1024
	realtimeGatewayConcurrency = 1000
	realtimeGatewayMaxSockets  = 950
	realtimeGatewayInstances   = 1
	realtimeGatewayTimeout     = maxRequestTimeout

	realtimeKeysVolume = "realtime-keys"
	realtimeKeysDir    = "/var/run/ocel/realtime"
	realtimeKeysFile   = "keys.json"

	realtimeImageName   = "ocel-realtime"
	realtimeImagePath   = "/realtime-gateway"
	realtimeImageTagLen = 32
)

var realtimeImageTag = sync.OnceValue(func() string {
	sum := sha256.New()
	sum.Write([]byte(staticImage + "\x00"))
	sum.Write(payloads.RealtimeGateway())
	return hex.EncodeToString(sum.Sum(nil))[:realtimeImageTagLen]
})

func realtimeGatewayServing(names Names, ref provider.StackRef, host, image, inlineKeys string) serving {
	gateway := serving{
		service:     names.RealtimeGateway(ref.Project, ref.Name.Env),
		labels:      stackLabels(names, ref),
		image:       image,
		account:     names.RealtimeAccountEmail(ref.Tier),
		compute:     provider.ComputeServerless,
		public:      true,
		ingress:     ingressEverywhere,
		cpu:         realtimeGatewayCPU,
		memory:      realtimeGatewayMemoryMiB,
		concurrency: realtimeGatewayConcurrency,
		instances:   provider.Instances{Max: realtimeGatewayInstances},
		timeout:     realtimeGatewayTimeout,
		env: map[string]string{
			gatewayenv.HostVar:       host,
			gatewayenv.AnyOriginVar:  strconv.FormatBool(true),
			gatewayenv.MaxSocketsVar: strconv.Itoa(realtimeGatewayMaxSockets),
		},
	}
	if inlineKeys != "" {
		gateway.env[gatewayenv.KeysVar] = inlineKeys
		return gateway
	}
	keys := secretMount{name: realtimeKeysVolume, secret: names.RealtimeKeysSecret(ref.Tier, ref.Project, ref.Name.Env), dir: realtimeKeysDir, file: realtimeKeysFile}
	gateway.mounts = []secretMount{keys}
	gateway.env[gatewayenv.KeysFileVar] = keys.dir + "/" + keys.file
	return gateway
}

func (r realtimeEnvironment) gatewayImage() string {
	return r.clients.RepositoryPath(r.clients.region, r.ref.Tier) + "/" + realtimeImageName + ":" + realtimeImageTag()
}

func (r realtimeEnvironment) readGateway(ctx context.Context) (*run.GoogleCloudRunV2Service, error) {
	services, err := r.clients.Run()
	if err != nil {
		return nil, err
	}
	name := r.clients.RealtimeGateway(r.ref.Project, r.ref.Name.Env)
	found, err := attempted(ctx, func(call ...googleapi.CallOption) (*run.GoogleCloudRunV2Service, error) {
		return services.Projects.Locations.Services.Get(r.clients.servicePath(name)).Context(ctx).Do(call...)
	})
	if absent(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the realtime gateway %s: %w", name, err)
	}
	return found, nil
}

func (r realtimeEnvironment) serveGateway(ctx context.Context, keys map[string]string, progress progress.Log) (string, error) {
	inlineKeys := ""
	if r.keysInEnv {
		encoded, err := json.Marshal(keys)
		if err != nil {
			return "", err
		}
		inlineKeys = string(encoded)
	}
	current, err := r.readGateway(ctx)
	if err != nil {
		return "", err
	}
	image := r.gatewayImage()
	if current == nil || imageOf(current) != image {
		if err := r.pushBinary(ctx, r.ref.Tier, realtimeImageName, image, payloads.RealtimeGateway(), realtimeImagePath); err != nil {
			return "", err
		}
	}
	if current == nil {
		if err := r.refuseUnbootstrappedTier(ctx); err != nil {
			return "", err
		}
	}
	name := r.clients.RealtimeGateway(r.ref.Project, r.ref.Name.Env)
	host := name
	if current != nil && hostOf(current.Uri) != "" {
		host = hostOf(current.Uri)
	}
	for range 2 {
		desired := realtimeGatewayServing(r.clients.Names, r.ref, host, image, inlineKeys)
		wanted, err := serviceOf(desired)
		if err != nil {
			return "", err
		}
		if current != nil && sameGateway(current, wanted) {
			return host, nil
		}
		ran, err := r.deployService(ctx, desired, progress)
		if err != nil {
			return "", err
		}
		served := hostOf(ran.url)
		if served == "" {
			return "", refusal.Refuse(refusal.CodeNotReady,
				"Cloud Run gave the realtime gateway %s no URL, and a realtime binding names the host browsers connect to", name)
		}
		if served != host {
			host, current = served, nil
			continue
		}
		return host, nil
	}
	return "", refusal.Refuse(refusal.CodeBusy,
		"Cloud Run answered the realtime gateway %s on another host each time it was released, and a realtime binding names the one host browsers connect to\n"+
			"Deploy again once the service settles",
		name)
}

func (r realtimeEnvironment) refuseUnbootstrappedTier(ctx context.Context) error {
	accounts, err := r.clients.Accounts()
	if err != nil {
		return err
	}
	account := r.clients.RealtimeAccount(r.ref.Tier)
	_, err = attempted(ctx, accounts.Projects.ServiceAccounts.Get(accountPath(r.clients, account)).Context(ctx).Do)
	if absent(err) {
		return refusal.Refuse(refusal.CodeNotReady,
			"the realtime gateways of tier %s run as the %s service account, and project %s has none: the tier was bootstrapped before realtime was served here.\n"+
				"Run `ocel bootstrap` for this tier again, then deploy",
			r.ref.Tier, account, r.clients.project)
	}
	if err != nil {
		return fmt.Errorf("read the %s service account the realtime gateways of tier %s run as: %w", account, r.ref.Tier, err)
	}
	return nil
}

func imageOf(service *run.GoogleCloudRunV2Service) string {
	if service.Template == nil || len(service.Template.Containers) != 1 {
		return ""
	}
	return service.Template.Containers[0].Image
}

func sameGateway(current, desired *run.GoogleCloudRunV2Service) bool {
	if current.Template == nil || len(current.Template.Containers) != 1 || current.Template.Containers[0].Resources == nil {
		return false
	}
	template, wanted := current.Template, desired.Template
	container, want := template.Containers[0], wanted.Containers[0]
	scaling := template.Scaling
	if scaling == nil {
		scaling = &run.GoogleCloudRunV2RevisionScaling{}
	}
	return container.Image == want.Image &&
		template.ServiceAccount == wanted.ServiceAccount &&
		current.Ingress == desired.Ingress &&
		current.InvokerIamDisabled == desired.InvokerIamDisabled &&
		container.Resources.CpuIdle == want.Resources.CpuIdle &&
		sameLimits(container.Resources.Limits, want.Resources.Limits) &&
		scaling.MinInstanceCount == wanted.Scaling.MinInstanceCount &&
		scaling.MaxInstanceCount == wanted.Scaling.MaxInstanceCount &&
		template.MaxInstanceRequestConcurrency == wanted.MaxInstanceRequestConcurrency &&
		sameTimeout(template.Timeout, wanted.Timeout) &&
		slices.EqualFunc(container.Env, want.Env, func(a, b *run.GoogleCloudRunV2EnvVar) bool {
			return a.Name == b.Name && a.Value == b.Value
		}) &&
		slices.EqualFunc(template.Volumes, wanted.Volumes, sameSecretVolume) &&
		slices.EqualFunc(container.VolumeMounts, want.VolumeMounts, func(a, b *run.GoogleCloudRunV2VolumeMount) bool {
			return a.Name == b.Name && a.MountPath == b.MountPath
		})
}

func sameSecretVolume(a, b *run.GoogleCloudRunV2Volume) bool {
	if a.Name != b.Name || a.Secret == nil || b.Secret == nil || revisionName(a.Secret.Secret) != revisionName(b.Secret.Secret) {
		return false
	}
	return slices.EqualFunc(a.Secret.Items, b.Secret.Items, func(x, y *run.GoogleCloudRunV2VersionToPath) bool {
		return x.Version == y.Version && x.Path == y.Path
	})
}

func (r realtimeEnvironment) removeGateway(ctx context.Context, progress progress.Log) error {
	return r.tearDown(ctx, r.clients.RealtimeGateway(r.ref.Project, r.ref.Name.Env), progress)
}
