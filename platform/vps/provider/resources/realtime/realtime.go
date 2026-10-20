package realtime

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/platform/realtime/gatewayenv"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/component"
	"github.com/ocelhq/ocel/platform/vps/provider/docker"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/resources/container"
)

const (
	gatewayResource = "ocel-realtime"
	gatewayOwner    = "ocel"
	gatewayKind     = "realtime"
	gatewayUser     = "65532:65532"
	gatewayRuns     = 3
	signingKind     = "signing-key"
	signingName     = "signing-key"
	labelBinary     = "ocel.binary"

	GatewayMemoryBytes = 256 << 20
	SocketBytes        = 64 << 10
)

type Resource struct{ box component.Box }

func New(box component.Box) Resource { return Resource{box: box} }

func gatewayName(ref provider.StackRef) (string, error) {
	return container.Name(ref, gatewayOwner, gatewayKind)
}

func gatewayAddress(name string) string {
	return name + ":" + gatewayenv.ListenPort
}

func FindPublishURL(bindings []provider.Binding) string {
	at := slices.IndexFunc(bindings, func(binding provider.Binding) bool { return binding.Type == provider.BindingRealtime })
	if at < 0 {
		return ""
	}
	return "http://" + bindings[at].Properties[provider.PropertyHost] + gatewayenv.PublishPath
}

func newGateway(ref provider.StackRef, keys map[string]string) (container.Spec, error) {
	encoded, err := json.Marshal(keys)
	if err != nil {
		return container.Spec{}, err
	}
	name, err := gatewayName(ref)
	if err != nil {
		return container.Spec{}, err
	}
	return container.Spec{
		Name:     name,
		Project:  ref.Project,
		Resource: gatewayResource,
		Tier:     ref.Tier,

		Image:    docker.StaticImage,
		Args:     []string{host.RealtimeMounted},
		User:     gatewayUser,
		ReadOnly: true,
		Memory:   strconv.Itoa(GatewayMemoryBytes),
		Mounts:   []container.Mount{{Source: host.RealtimeDir, Target: host.RealtimeMount}},
		Labels:   map[string]string{labelBinary: host.RealtimeBinarySum()},
		Env: map[string]string{
			gatewayenv.HostVar:       gatewayAddress(name),
			gatewayenv.KeysVar:       string(encoded),
			gatewayenv.MaxSocketsVar: strconv.Itoa(GatewayMemoryBytes / SocketBytes),
		},
		Ready: []string{host.RealtimeMounted, "ready"},
	}, nil
}

func mintSigningSeed() (string, error) {
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return "", fmt.Errorf("mint a signing key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(seed), nil
}

func nameSigningKey(ref provider.StackRef, binding string) (string, error) {
	return container.Name(ref, binding, signingKind)
}

func refuseUnconfigured(in resources.ProvisionRequest) error {
	if in.Resource.Realtime != nil {
		return nil
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"realtime %s reached the box with no config, so nothing knows its channels", in.Resource.Declared)
}

type namespaceRecord struct {
	key       records.Key
	namespace string
	verifyKey string
	value     []byte
}

func newNamespaceKey(in resources.ProvisionRequest) (string, records.Key) {
	namespace := component.ChooseDeclaredName(in.Resource.Declared, in.Resource.Name)
	return namespace, boxapi.RealtimeNamespaceKey(in.Ref.Tier, in.Ref.Project, in.Ref.Name.Env, namespace)
}

func newNamespaceRecord(in resources.ProvisionRequest, seed []byte) (namespaceRecord, error) {
	verifyKey := base64.StdEncoding.EncodeToString(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey))
	value, err := json.Marshal(boxapi.RealtimeNamespace{VerifyKey: verifyKey})
	if err != nil {
		return namespaceRecord{}, err
	}
	namespace, key := newNamespaceKey(in)
	return namespaceRecord{
		key:       key,
		namespace: namespace,
		verifyKey: verifyKey,
		value:     value,
	}, nil
}

func newBinding(in resources.ProvisionRequest, seed []byte, verifyKey string) (provider.Binding, error) {
	gateway, err := gatewayName(in.Ref)
	if err != nil {
		return provider.Binding{}, err
	}
	return provider.Binding{
		Type:     provider.BindingRealtime,
		Name:     in.Resource.Name,
		Resource: in.Resource.Declared,
		Properties: map[string]string{
			provider.PropertyURL:        boxapi.RealtimeSocketPath,
			provider.PropertyHost:       gatewayAddress(gateway),
			provider.PropertySigningKey: base64.StdEncoding.EncodeToString(seed),
			provider.PropertyVerifyKey:  verifyKey,
		},
	}, nil
}

func (r Resource) Plan(ctx context.Context, in resources.ProvisionRequest, recorded *provider.Binding) (provider.ChangeAction, string, error) {
	if err := refuseUnconfigured(in); err != nil {
		return "", "", err
	}
	bound, err := newSigningAssociatedData(in)
	if err != nil {
		return "", "", err
	}
	signingKey, err := nameSigningKey(in.Ref, in.Resource.Name)
	if err != nil {
		return "", "", err
	}
	sealed, opened, err := r.box.ReadKeptSealed(ctx, in.Ref.Tier, signingKey, in.Resource.Name, bound)
	if err != nil {
		return "", "", err
	}
	_, key := newNamespaceKey(in)
	held, err := records.ReadOrEmpty(ctx, r.box.KeyValues, key)
	if err != nil {
		return "", "", err
	}
	if len(sealed) == 0 {
		if len(held.Value) == 0 {
			return provider.ActionCreate, "", nil
		}
		gateway, err := gatewayName(in.Ref)
		if err != nil {
			return "", "", err
		}
		return provider.ActionUpdate, "the box keeps no signing key for it, so a new one is minted and its gateway " + gateway + " restarted to verify it", nil
	}
	seed, err := decodeSigningSeed(in, signingKey, opened)
	if err != nil {
		return "", "", err
	}
	record, err := newNamespaceRecord(in, seed)
	if err != nil {
		return "", "", err
	}
	var change component.Change
	switch {
	case len(held.Value) == 0:
		change.Add(provider.ActionCreate, "")
	case string(held.Value) != string(record.value):
		change.Add(provider.ActionUpdate, "its verify key is recorded anew")
	}
	keys, err := r.readKeys(ctx, in.Ref)
	if err != nil {
		return "", "", err
	}
	keys[record.namespace] = record.verifyKey
	gateway, err := newGateway(in.Ref, keys)
	if err != nil {
		return "", "", err
	}
	action, reason, err := r.box.Containers.Plan(ctx, gateway)
	if err != nil {
		return "", "", err
	}
	switch action {
	case provider.ActionKeep:
	case provider.ActionCreate:
		change.Add(provider.ActionUpdate, "its gateway "+gateway.Name+" is started")
	default:
		change.Add(provider.ActionUpdate, "its gateway "+gateway.Name+" is replaced: "+reason)
	}
	binding, err := newBinding(in, seed, record.verifyKey)
	if err != nil {
		return "", "", err
	}
	change.AddRecorded(recorded, binding)
	return change.Report()
}

func (r Resource) Provision(ctx context.Context, in resources.ProvisionRequest, progress progress.Log) (provider.Binding, error) {
	if err := refuseUnconfigured(in); err != nil {
		return provider.Binding{}, err
	}
	seed, err := r.signingSeed(ctx, in)
	if err != nil {
		return provider.Binding{}, err
	}
	record, err := newNamespaceRecord(in, seed)
	if err != nil {
		return provider.Binding{}, err
	}
	if err := records.Change(ctx, r.box.KeyValues, record.key, func(recorded records.Entry) ([]byte, bool, error) {
		return record.value, string(recorded.Value) != string(record.value), nil
	}); err != nil {
		return provider.Binding{}, err
	}
	if err := r.runGateway(ctx, in.Ref, progress); err != nil {
		return provider.Binding{}, err
	}
	return newBinding(in, seed, record.verifyKey)
}

func newSigningAssociatedData(in resources.ProvisionRequest) (seal.AssociatedData, error) {
	return boxapi.NewSecretAssociatedData(in.Ref.Project, in.Ref.Tier, in.Ref.Name.String(), component.SecretFolder, in.Resource.Name, signingName)
}

func (r Resource) signingSeed(ctx context.Context, in resources.ProvisionRequest) ([]byte, error) {
	bound, err := newSigningAssociatedData(in)
	if err != nil {
		return nil, err
	}
	signingKey, err := nameSigningKey(in.Ref, in.Resource.Name)
	if err != nil {
		return nil, err
	}
	_, opened, err := r.box.KeptSealed(ctx, in.Ref.Tier, signingKey, in.Resource.Name, bound, mintSigningSeed)
	if err != nil {
		return nil, err
	}
	return decodeSigningSeed(in, signingKey, opened)
}

func decodeSigningSeed(in resources.ProvisionRequest, signingKey, opened string) ([]byte, error) {
	seed, err := base64.StdEncoding.DecodeString(opened)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, refusal.Refuse(refusal.CodeNotReady,
			"the signing key kept for realtime %s is no Ed25519 seed\nRemove %s on the box",
			in.Resource.Declared, container.KeptPath(in.Ref.Tier, signingKey))
	}
	return seed, nil
}

func (r Resource) readKeys(ctx context.Context, ref provider.StackRef) (map[string]string, error) {
	partition, under := boxapi.RealtimeNamespacesUnder(ref.Tier, ref.Project, ref.Name.Env)
	entries, err := r.box.KeyValues.List(ctx, partition, under...)
	if err != nil {
		return nil, err
	}
	keys := map[string]string{}
	for _, entry := range entries {
		if len(entry.Key.Path) != len(under)+1 {
			continue
		}
		var recorded boxapi.RealtimeNamespace
		if err := json.Unmarshal(entry.Value, &recorded); err != nil {
			return nil, fmt.Errorf("read %s: %w", entry.Key, err)
		}
		keys[entry.Key.Path[len(under)]] = recorded.VerifyKey
	}
	return keys, nil
}

func (r Resource) runGateway(ctx context.Context, ref provider.StackRef, progress progress.Log) error {
	keys, err := r.readKeys(ctx, ref)
	if err != nil {
		return err
	}
	for range gatewayRuns {
		if err := r.placeGateway(ctx, ref, keys, progress); err != nil {
			return err
		}
		placed := keys
		if keys, err = r.readKeys(ctx, ref); err != nil {
			return err
		}
		if maps.Equal(placed, keys) {
			return nil
		}
	}
	gateway, err := gatewayName(ref)
	if err != nil {
		return err
	}
	return refusal.Refuse(refusal.CodeBusy,
		"the realtime of %s changed each of the %d times its gateway %s was restarted to match, so another deploy is changing it\nDeploy again once that deploy is done",
		ref.Name.Env, gatewayRuns, gateway)
}

func (r Resource) placeGateway(ctx context.Context, ref provider.StackRef, keys map[string]string, progress progress.Log) error {
	if len(keys) == 0 {
		gateway, err := gatewayName(ref)
		if err != nil {
			return err
		}
		progress.Say("Removing the realtime gateway " + gateway + ": " + ref.Name.Env + " declares no realtime any more")
		return r.box.Containers.Remove(ctx, container.Ref{Tier: ref.Tier, Project: ref.Project, Resource: gatewayResource, Name: gateway})
	}
	gateway, err := newGateway(ref, keys)
	if err != nil {
		return err
	}
	progress.Say("Serving realtime " + strings.Join(slices.Sorted(maps.Keys(keys)), ", ") + " from the gateway in container " + gateway.Name)
	return r.box.Containers.Run(ctx, gateway, "")
}

func (r Resource) Remove(ctx context.Context, ref provider.StackRef, binding provider.Binding, progress progress.Log) error {
	namespace := component.ChooseDeclaredName(binding.Resource, binding.Name)
	if err := records.Forget(ctx, r.box.KeyValues, boxapi.RealtimeNamespaceKey(ref.Tier, ref.Project, ref.Name.Env, namespace)); err != nil {
		return err
	}
	if err := r.runGateway(ctx, ref, progress); err != nil {
		return err
	}
	signingKey, err := nameSigningKey(ref, binding.Name)
	if err != nil {
		return err
	}
	return r.box.Containers.ForgetKept(ctx, ref.Tier, []string{signingKey})
}
