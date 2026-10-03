package gcp

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/progress"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/realtime/gatewayenv"
)

const realtimeServeRuns = 3

type realtimeNamespace struct {
	VerifyKey string `json:"verifyKey"`
}

type realtimeEnvironment struct {
	clients       *clients
	records       keyvalue.Store
	ref           provider.StackRef
	keysInEnv     bool
	pushBinary    func(ctx context.Context, tier environment.Tier, name, ref string, binary []byte, path string) error
	deployService func(ctx context.Context, s serving, progress progress.Log) (release, error)
	tearDown      func(ctx context.Context, service string, progress progress.Log) error
}

func (p *Provider) openRealtime(ctx context.Context, ref provider.StackRef) (realtimeEnvironment, error) {
	opened, err := p.openClients(ctx)
	if err != nil {
		return realtimeEnvironment{}, err
	}
	return realtimeEnvironment{
		clients: opened,
		records: p.KeyValues(),
		ref:     ref,
		// HACK: floci-gcp runs no secret volume, so an emulated gateway takes its keys from its environment and a change of keys rolls a revision; drop keysInEnv once floci-gcp mounts secret volumes (#1605).
		keysInEnv:     p.emulated(),
		pushBinary:    p.pushBinary,
		deployService: p.deployService,
		tearDown:      p.tearDown,
	}, nil
}

func (p *Provider) ProvisionRealtime(ctx context.Context, in resources.ProvisionRequest, progress progress.Log) (provider.Binding, error) {
	environment, err := p.openRealtime(ctx, in.Ref)
	if err != nil {
		return provider.Binding{}, err
	}
	return environment.provision(ctx, in.Resource, progress)
}

func (p *Provider) removeRealtime(ctx context.Context, ref provider.StackRef, binding provider.Binding, progress progress.Log) error {
	environment, err := p.openRealtime(ctx, ref)
	if err != nil {
		return err
	}
	return environment.remove(ctx, binding, progress)
}

func findRealtimePublishURL(bindings []provider.Binding) string {
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

func (r realtimeEnvironment) namespaceKey(namespace string) keyvalue.Key {
	return keyvalue.Partition{Tier: r.ref.Tier, Root: keyvalue.RootRealtime}.Key(r.ref.Project, r.ref.Name.Env, namespace)
}

func (r realtimeEnvironment) provision(ctx context.Context, resource provider.Resource, progress progress.Log) (provider.Binding, error) {
	if resource.Realtime == nil {
		return provider.Binding{}, refusal.Refuse(refusal.CodeInvalid,
			"realtime %s reached the provider with no config, so nothing knows its channels", resource.Declared)
	}
	namespace := realtimeNamespaceOf(resource.Declared, resource.Name)
	seed, err := r.ensureSigningSeed(ctx, namespace)
	if err != nil {
		return provider.Binding{}, err
	}
	verifyKey := base64.StdEncoding.EncodeToString(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey))
	record, err := json.Marshal(realtimeNamespace{VerifyKey: verifyKey})
	if err != nil {
		return provider.Binding{}, err
	}
	if err := keyvalue.Change(ctx, r.records, r.namespaceKey(namespace), func(recorded keyvalue.Entry) ([]byte, bool, error) {
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
			provider.PropertyTransport:  bindingsv1.RealtimeTransport_REALTIME_TRANSPORT_OCEL_GATEWAY.String(),
			provider.PropertyURL:        "wss://" + host + gatewayenv.SocketPath,
			provider.PropertyHost:       host,
			provider.PropertySigningKey: base64.StdEncoding.EncodeToString(seed),
			provider.PropertyVerifyKey:  verifyKey,
		},
	}, nil
}

func (r realtimeEnvironment) remove(ctx context.Context, binding provider.Binding, progress progress.Log) error {
	namespace := realtimeNamespaceOf(binding.Resource, binding.Name)
	if err := keyvalue.Forget(ctx, r.records, r.namespaceKey(namespace)); err != nil {
		return err
	}
	if _, err := r.serve(ctx, progress); err != nil {
		return err
	}
	return r.removeSigningKey(ctx, namespace)
}

func (r realtimeEnvironment) readKeys(ctx context.Context) (map[string]string, error) {
	under := []string{r.ref.Project, r.ref.Name.Env}
	entries, err := r.records.List(ctx, keyvalue.Partition{Tier: r.ref.Tier, Root: keyvalue.RootRealtime}, under...)
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
		ensureProgress(progress).Say("Removing the realtime gateway " + gateway + ": " + r.ref.Name.Env + " declares no realtime any more")
		if err := r.removeGateway(ctx, progress); err != nil {
			return "", err
		}
		return "", r.removeKeys(ctx)
	}
	if err := r.writeKeys(ctx, keys); err != nil {
		return "", err
	}
	ensureProgress(progress).Debug("Serving realtime " + strings.Join(slices.Sorted(maps.Keys(keys)), ", ") + " from the gateway " + gateway)
	return r.serveGateway(ctx, keys, progress)
}
