package realtime

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/component"
	"github.com/ocelhq/ocel/platform/gcp/provider/cost"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
	"github.com/ocelhq/ocel/platform/realtime/gatewayenv"
)

const RootRealtime records.Root = "realtime"

const realtimeServeRuns = 3

type realtimeNamespace struct {
	VerifyKey string `json:"verifyKey"`
}

type realtimeEnvironment struct {
	clients        *gcloud.Clients
	records        records.Store
	ref            provider.StackRef
	keysInEnv      bool
	pubsubEndpoint string
	pushBinary     func(ctx context.Context, tier environment.Tier, name, ref string, binary []byte, path string) error
	deployAndRoute func(ctx context.Context, s run.Serving, progress progress.Log) (run.Release, error)
	tearDown       func(ctx context.Context, service string, progress progress.Log) error
}

type Realtime struct {
	openClients func(ctx context.Context) (*gcloud.Clients, error)
	records     records.Store
	services    *run.Services
	pushBinary  func(ctx context.Context, tier environment.Tier, name, ref string, binary []byte, path string) error

	emulatorEndpoint string
}

func New(
	openClients func(ctx context.Context) (*gcloud.Clients, error),
	store records.Store,
	services *run.Services,
	pushBinary func(ctx context.Context, tier environment.Tier, name, ref string, binary []byte, path string) error,
	emulatorEndpoint string,
) *Realtime {
	return &Realtime{openClients: openClients, records: store, services: services, pushBinary: pushBinary, emulatorEndpoint: emulatorEndpoint}
}

func (p *Realtime) Component() component.Component {
	return component.Component{
		Types:    []provider.BindingType{provider.BindingRealtime},
		Hooks:    resources.ResourceHooks{Plan: p.Plan, Provision: p.Provision, Remove: p.Remove},
		Shape:    shapeRealtime,
		Accounts: []bootstrap.Account{{Name: names.Names.RealtimeAccount, Purpose: Purpose}},
	}
}

func (p *Realtime) openRealtime(ctx context.Context, ref provider.StackRef) (realtimeEnvironment, error) {
	opened, err := p.openClients(ctx)
	if err != nil {
		return realtimeEnvironment{}, err
	}
	return realtimeEnvironment{
		clients: opened,
		records: p.records,
		ref:     ref,
		// HACK: floci-gcp runs no secret volume, so an emulated gateway takes its keys from its environment and a change of keys rolls a revision; drop keysInEnv once floci-gcp mounts secret volumes (#1605).
		keysInEnv:      p.emulatorEndpoint != "",
		pubsubEndpoint: p.emulatorEndpoint,
		pushBinary:     p.pushBinary,
		deployAndRoute: p.services.DeployAndRoute,
		tearDown:       p.services.TearDown,
	}, nil
}

func (p *Realtime) Provision(ctx context.Context, in resources.ProvisionRequest, progress progress.Log) (provider.Binding, error) {
	environment, err := p.openRealtime(ctx, in.Ref)
	if err != nil {
		return provider.Binding{}, err
	}
	return environment.provision(ctx, in.Resource, progress)
}

func (p *Realtime) Plan(ctx context.Context, in resources.ProvisionRequest, _ *provider.Binding) (provider.ChangeAction, string, error) {
	environment, err := p.openRealtime(ctx, in.Ref)
	if err != nil {
		return "", "", err
	}
	return environment.plan(ctx, in.Resource)
}

func (r realtimeEnvironment) plan(ctx context.Context, resource provider.Resource) (provider.ChangeAction, string, error) {
	if err := refuseUnconfiguredRealtime(resource); err != nil {
		return "", "", err
	}
	namespace := realtimeNamespaceOf(resource.Declared, resource.Name)
	service, err := r.secrets()
	if err != nil {
		return "", "", err
	}
	seed, found, err := r.readSigningSeed(ctx, service, namespace)
	if err != nil {
		return "", "", err
	}
	if !found {
		return provider.ActionCreate, "", nil
	}
	verifyKey := deriveVerifyKey(seed)
	record, err := encodeNamespaceRecord(verifyKey)
	if err != nil {
		return "", "", err
	}
	var differences []string
	recorded, err := records.ReadOrEmpty(ctx, r.records, r.namespaceKey(namespace))
	if err != nil {
		return "", "", err
	}
	if string(recorded.Value) != string(record) {
		differences = append(differences, "realtime "+namespace+"'s verify key is recorded for its gateway to trust")
	}
	keys, err := r.readKeys(ctx)
	if err != nil {
		return "", "", err
	}
	keys[namespace] = verifyKey
	secret, err := r.readKeysSecret(ctx, service, keys)
	if err != nil {
		return "", "", err
	}
	if difference := secret.describeChange(keys); difference != "" {
		differences = append(differences, difference)
	}
	difference, err := r.readTopicDifference(ctx)
	if err != nil {
		return "", "", err
	}
	if difference != "" {
		differences = append(differences, difference)
	}
	if difference, err = r.readGatewayDifference(ctx, keys); err != nil {
		return "", "", err
	}
	if difference != "" {
		differences = append(differences, difference)
	}
	if len(differences) > 0 {
		return provider.ActionUpdate, strings.Join(differences, "; "), nil
	}
	return provider.ActionKeep, provider.ReasonCurrent, nil
}

func refuseUnconfiguredRealtime(resource provider.Resource) error {
	if resource.Realtime != nil {
		return nil
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"realtime %s reached the provider with no config, so nothing knows its channels", resource.Declared)
}

func deriveVerifyKey(seed []byte) string {
	return base64.StdEncoding.EncodeToString(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey))
}

func encodeNamespaceRecord(verifyKey string) ([]byte, error) {
	return json.Marshal(realtimeNamespace{VerifyKey: verifyKey})
}

func (p *Realtime) Remove(ctx context.Context, ref provider.StackRef, binding provider.Binding, progress progress.Log) error {
	environment, err := p.openRealtime(ctx, ref)
	if err != nil {
		return err
	}
	return environment.remove(ctx, binding, progress)
}

func FindPublishURL(bindings []provider.Binding) string {
	at := slices.IndexFunc(bindings, func(binding provider.Binding) bool { return binding.Type == provider.BindingRealtime })
	if at < 0 {
		return ""
	}
	return "https://" + bindings[at].Properties[provider.PropertyHost] + gatewayenv.PublishPath
}

func realtimeNamespaceOf(declared, name string) string {
	if declared != "" {
		return declared
	}
	return name
}

func (r realtimeEnvironment) namespaceKey(namespace string) records.Key {
	return records.Partition{Tier: r.ref.Tier, Root: RootRealtime}.Key(r.ref.Project, r.ref.Name.Env, namespace)
}

func (r realtimeEnvironment) provision(ctx context.Context, resource provider.Resource, progress progress.Log) (provider.Binding, error) {
	if err := refuseUnconfiguredRealtime(resource); err != nil {
		return provider.Binding{}, err
	}
	namespace := realtimeNamespaceOf(resource.Declared, resource.Name)
	seed, err := r.ensureSigningSeed(ctx, namespace)
	if err != nil {
		return provider.Binding{}, err
	}
	verifyKey := deriveVerifyKey(seed)
	record, err := encodeNamespaceRecord(verifyKey)
	if err != nil {
		return provider.Binding{}, err
	}
	if err := records.Change(ctx, r.records, r.namespaceKey(namespace), func(recorded records.Entry) ([]byte, bool, error) {
		return record, string(recorded.Value) != string(record), nil
	}); err != nil {
		return provider.Binding{}, err
	}
	host, err := r.serve(ctx, progress)
	if err != nil {
		return provider.Binding{}, err
	}
	return provider.Binding{
		Type:     provider.BindingRealtime,
		Name:     resource.Name,
		Resource: resource.Declared,
		Properties: map[string]string{
			provider.PropertyURL:        "wss://" + host + gatewayenv.SocketPath,
			provider.PropertyHost:       host,
			provider.PropertySigningKey: base64.StdEncoding.EncodeToString(seed),
			provider.PropertyVerifyKey:  verifyKey,
		},
	}, nil
}

func (r realtimeEnvironment) remove(ctx context.Context, binding provider.Binding, progress progress.Log) error {
	namespace := realtimeNamespaceOf(binding.Resource, binding.Name)
	if err := records.Forget(ctx, r.records, r.namespaceKey(namespace)); err != nil {
		return err
	}
	if _, err := r.serve(ctx, progress); err != nil {
		return err
	}
	return r.removeSigningKey(ctx, namespace)
}

func (r realtimeEnvironment) readKeys(ctx context.Context) (map[string]string, error) {
	under := []string{r.ref.Project, r.ref.Name.Env}
	entries, err := r.records.List(ctx, records.Partition{Tier: r.ref.Tier, Root: RootRealtime}, under...)
	if err != nil {
		return nil, err
	}
	keys := map[string]string{}
	for _, entry := range entries {
		if len(entry.Key.Path) != len(under)+1 {
			continue
		}
		var recorded realtimeNamespace
		if err := json.Unmarshal(entry.Value, &recorded); err != nil {
			return nil, fmt.Errorf("read %s: %w", entry.Key, err)
		}
		keys[entry.Key.Path[len(under)]] = recorded.VerifyKey
	}
	return keys, nil
}

func (r realtimeEnvironment) serve(ctx context.Context, progress progress.Log) (string, error) {
	keys, err := r.readKeys(ctx)
	if err != nil {
		return "", err
	}
	for range realtimeServeRuns {
		host, err := r.place(ctx, keys, progress)
		if err != nil {
			return "", err
		}
		placed := keys
		if keys, err = r.readKeys(ctx); err != nil {
			return "", err
		}
		if maps.Equal(placed, keys) {
			return host, nil
		}
	}
	return "", refusal.Refuse(refusal.CodeBusy,
		"the realtime of %s changed each of the %d times its keys were written to match, so another deploy is changing it\nDeploy again once that deploy is done",
		r.ref.Name.Env, realtimeServeRuns)
}

func (r realtimeEnvironment) place(ctx context.Context, keys map[string]string, progress progress.Log) (string, error) {
	gateway := r.clients.RealtimeGateway(r.ref.Project, r.ref.Name.Env)
	if len(keys) == 0 {
		gcloud.EnsureProgress(progress).Say("Removing the realtime gateway " + gateway + ": " + r.ref.Name.Env + " declares no realtime any more")
		if err := r.removeGateway(ctx, progress); err != nil {
			return "", err
		}
		if err := r.removeTopic(ctx); err != nil {
			return "", err
		}
		return "", r.removeKeys(ctx)
	}
	if err := r.writeKeys(ctx, keys); err != nil {
		return "", err
	}
	if err := r.ensureTopic(ctx); err != nil {
		return "", err
	}
	gcloud.EnsureProgress(progress).Debug("Serving realtime " + strings.Join(slices.Sorted(maps.Keys(keys)), ", ") + " from the gateway " + gateway)
	return r.serveGateway(ctx, keys, progress)
}

func shapeRealtime(shape component.Shape, declared []provider.Resource) error {
	tree, names, req, region, environment := shape.Tree, shape.Names, shape.Request, shape.Region, shape.Environment
	secret, _ := bootstrap.Resource{Kind: bootstrap.KindSecret}.Shape(region)
	for _, resource := range declared {
		namespace := realtimeNamespaceOf(resource.Declared, resource.Name)
		tree.Add(environment, shape.Vendor, cost.SecretManagerSecretType, names.RealtimeSigningSecret(req.Deploy.Slug, req.Deploy.Env, namespace), region, secret.Properties)
	}
	tree.Add(environment, shape.Vendor, cost.SecretManagerSecretType, names.RealtimeKeysSecret(req.Deploy.Tier, req.Deploy.Slug, req.Deploy.Env), region, secret.Properties)
	gateway := names.RealtimeGateway(req.Deploy.Slug, req.Deploy.Env)
	tree.Add(environment, shape.Vendor, cost.PubSubTopicType, gateway, region, map[string]any{})
	tree.Add(environment, shape.Vendor, cost.PubSubSubscriptionType, gateway, region, map[string]any{})
	tree.Add(environment, shape.Vendor, cost.CloudRunServiceType, gateway, region, map[string]any{
		"location":      region,
		"ingress":       run.IngressEverywhere,
		"holds_sockets": true,
		"template": map[string]any{
			"scaling":                          map[string]any{"min_instance_count": 0, "max_instance_count": realtimeGatewayInstances},
			"max_instance_request_concurrency": realtimeGatewayConcurrency,
			"containers": []any{map[string]any{
				"resources": map[string]any{
					"cpu_idle": true,
					"limits":   map[string]any{"cpu": realtimeGatewayCPU, "memory": strconv.Itoa(realtimeGatewayMemoryMiB) + "Mi"},
				},
			}},
		},
	})
	return nil
}
