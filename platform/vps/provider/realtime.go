package vps

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
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

const (
	realtimeGatewayResource = "ocel-realtime"
	realtimeGatewayOwner    = "ocel"
	realtimeGatewayKind     = "realtime"
	realtimeGatewayUser     = "65532:65532"
	realtimeGatewayRuns     = 3
	realtimeSigningKind     = "signing-key"
	realtimeSigningName     = "signing-key"

	realtimeGatewayMemoryBytes = 256 << 20
	realtimeSocketBytes        = 64 << 10
)

func realtimeGatewayName(ref provider.StackRef) string {
	return host.ResourceName(ref.Project, ref.Name.String(), realtimeGatewayOwner, realtimeGatewayKind)
}

func realtimeGatewayAddress(ref provider.StackRef) string {
	return realtimeGatewayName(ref) + ":" + gatewayenv.ListenPort
}

func findRealtimePublishURL(bindings []provider.Binding) string {
	at := slices.IndexFunc(bindings, func(binding provider.Binding) bool { return binding.Type == provider.BindingRealtime })
	if at < 0 {
		return ""
	}
	return "http://" + bindings[at].Properties[provider.PropertyHost] + gatewayenv.PublishPath
}

func newRealtimeGateway(ref provider.StackRef, keys map[string]string) (host.ResourceContainer, error) {
	encoded, err := json.Marshal(keys)
	if err != nil {
		return host.ResourceContainer{}, err
	}
	return host.ResourceContainer{
		Name:     realtimeGatewayName(ref),
		Project:  ref.Project,
		Resource: realtimeGatewayResource,
		Tier:     ref.Tier,

		Image:    host.StaticImage,
		Args:     []string{host.RealtimeMounted},
		User:     realtimeGatewayUser,
		ReadOnly: true,
		Memory:   strconv.Itoa(realtimeGatewayMemoryBytes),
		Mounts:   []host.Mount{{Source: host.RealtimeDir, Target: host.RealtimeMount}},
		Labels:   map[string]string{host.LabelBinary: host.RealtimeBinarySum()},
		Env: map[string]string{
			gatewayenv.HostVar:       realtimeGatewayAddress(ref),
			gatewayenv.KeysVar:       string(encoded),
			gatewayenv.MaxSocketsVar: strconv.Itoa(realtimeGatewayMemoryBytes / realtimeSocketBytes),
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

func nameSigningKey(ref provider.StackRef, binding string) string {
	return host.ResourceName(ref.Project, ref.Name.String(), binding, realtimeSigningKind)
}

func refuseUnconfiguredRealtime(in resources.ProvisionRequest) error {
	if in.Resource.Realtime != nil {
		return nil
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"realtime %s reached the box with no config, so nothing knows its channels", in.Resource.Declared)
}

type realtimeNamespaceRecord struct {
	key       records.Key
	namespace string
	verifyKey string
	value     []byte
}

func newRealtimeNamespaceKey(in resources.ProvisionRequest) (string, records.Key) {
	namespace := chooseDeclaredName(in.Resource.Declared, in.Resource.Name)
	return namespace, boxapi.RealtimeNamespaceKey(in.Ref.Tier, in.Ref.Project, in.Ref.Name.Env, namespace)
}

func newRealtimeNamespaceRecord(in resources.ProvisionRequest, seed []byte) (realtimeNamespaceRecord, error) {
	verifyKey := base64.StdEncoding.EncodeToString(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey))
	value, err := json.Marshal(boxapi.RealtimeNamespace{VerifyKey: verifyKey})
	if err != nil {
		return realtimeNamespaceRecord{}, err
	}
	namespace, key := newRealtimeNamespaceKey(in)
	return realtimeNamespaceRecord{
		key:       key,
		namespace: namespace,
		verifyKey: verifyKey,
		value:     value,
	}, nil
}

func newRealtimeBinding(in resources.ProvisionRequest, seed []byte, verifyKey string) provider.Binding {
	return provider.Binding{
		Type:     provider.BindingRealtime,
		Name:     in.Resource.Name,
		Resource: in.Resource.Declared,
		Properties: map[string]string{
			provider.PropertyURL:        boxapi.RealtimeSocketPath,
			provider.PropertyHost:       realtimeGatewayAddress(in.Ref),
			provider.PropertySigningKey: base64.StdEncoding.EncodeToString(seed),
			provider.PropertyVerifyKey:  verifyKey,
		},
	}
}

func (p *Provider) PlanRealtime(ctx context.Context, in resources.ProvisionRequest, recorded *provider.Binding) (provider.ChangeAction, string, error) {
	if err := refuseUnconfiguredRealtime(in); err != nil {
		return "", "", err
	}
	bound, err := newRealtimeSigningAssociatedData(in)
	if err != nil {
		return "", "", err
	}
	sealed, opened, err := p.readKeptSealed(ctx, in.Ref.Tier, nameSigningKey(in.Ref, in.Resource.Name), in.Resource.Name, bound)
	if err != nil {
		return "", "", err
	}
	_, key := newRealtimeNamespaceKey(in)
	held, err := records.ReadOrEmpty(ctx, p.keyValues, key)
	if err != nil {
		return "", "", err
	}
	if len(sealed) == 0 {
		if len(held.Value) == 0 {
			return provider.ActionCreate, "", nil
		}
		return provider.ActionUpdate, "the box keeps no signing key for it, so a new one is minted and its gateway " + realtimeGatewayName(in.Ref) + " restarted to verify it", nil
	}
	seed, err := decodeSigningSeed(in, opened)
	if err != nil {
		return "", "", err
	}
	record, err := newRealtimeNamespaceRecord(in, seed)
	if err != nil {
		return "", "", err
	}
	var change resourceChange
	switch {
	case len(held.Value) == 0:
		change.add(provider.ActionCreate, "")
	case string(held.Value) != string(record.value):
		change.add(provider.ActionUpdate, "its verify key is recorded anew")
	}
	keys, err := p.readRealtimeKeys(ctx, in.Ref)
	if err != nil {
		return "", "", err
	}
	keys[record.namespace] = record.verifyKey
	gateway, err := newRealtimeGateway(in.Ref, keys)
	if err != nil {
		return "", "", err
	}
	action, reason, err := p.host.PlanResource(ctx, gateway)
	if err != nil {
		return "", "", err
	}
	switch action {
	case provider.ActionKeep:
	case provider.ActionCreate:
		change.add(provider.ActionUpdate, "its gateway "+gateway.Name+" is started")
	default:
		change.add(provider.ActionUpdate, "its gateway "+gateway.Name+" is replaced: "+reason)
	}
	change.addRecorded(recorded, newRealtimeBinding(in, seed, record.verifyKey))
	return change.report()
}

func (p *Provider) ProvisionRealtime(ctx context.Context, in resources.ProvisionRequest, progress progress.Log) (provider.Binding, error) {
	if err := refuseUnconfiguredRealtime(in); err != nil {
		return provider.Binding{}, err
	}
	seed, err := p.realtimeSigningSeed(ctx, in)
	if err != nil {
		return provider.Binding{}, err
	}
	record, err := newRealtimeNamespaceRecord(in, seed)
	if err != nil {
		return provider.Binding{}, err
	}
	if err := records.Change(ctx, p.keyValues, record.key, func(recorded records.Entry) ([]byte, bool, error) {
		return record.value, string(recorded.Value) != string(record.value), nil
	}); err != nil {
		return provider.Binding{}, err
	}
	if err := p.runRealtimeGateway(ctx, in.Ref, progress); err != nil {
		return provider.Binding{}, err
	}
	return newRealtimeBinding(in, seed, record.verifyKey), nil
}

func newRealtimeSigningAssociatedData(in resources.ProvisionRequest) (seal.AssociatedData, error) {
	return boxapi.NewSecretAssociatedData(in.Ref.Project, in.Ref.Tier, in.Ref.Name.String(), resourceSecretFolder, in.Resource.Name, realtimeSigningName)
}

func (p *Provider) realtimeSigningSeed(ctx context.Context, in resources.ProvisionRequest) ([]byte, error) {
	bound, err := newRealtimeSigningAssociatedData(in)
	if err != nil {
		return nil, err
	}
	_, opened, err := p.keptSealed(ctx, in.Ref.Tier, nameSigningKey(in.Ref, in.Resource.Name), in.Resource.Name, bound, mintSigningSeed)
	if err != nil {
		return nil, err
	}
	return decodeSigningSeed(in, opened)
}

func decodeSigningSeed(in resources.ProvisionRequest, opened string) ([]byte, error) {
	seed, err := base64.StdEncoding.DecodeString(opened)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, refusal.Refuse(refusal.CodeNotReady,
			"the signing key kept for realtime %s is no Ed25519 seed\nRemove %s on the box",
			in.Resource.Declared, host.KeptPath(in.Ref.Tier, nameSigningKey(in.Ref, in.Resource.Name)))
	}
	return seed, nil
}

func (p *Provider) readRealtimeKeys(ctx context.Context, ref provider.StackRef) (map[string]string, error) {
	partition, under := boxapi.RealtimeNamespacesUnder(ref.Tier, ref.Project, ref.Name.Env)
	entries, err := p.keyValues.List(ctx, partition, under...)
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

func (p *Provider) runRealtimeGateway(ctx context.Context, ref provider.StackRef, progress progress.Log) error {
	keys, err := p.readRealtimeKeys(ctx, ref)
	if err != nil {
		return err
	}
	for range realtimeGatewayRuns {
		if err := p.placeRealtimeGateway(ctx, ref, keys, progress); err != nil {
			return err
		}
		placed := keys
		if keys, err = p.readRealtimeKeys(ctx, ref); err != nil {
			return err
		}
		if maps.Equal(placed, keys) {
			return nil
		}
	}
	return refusal.Refuse(refusal.CodeBusy,
		"the realtime of %s changed each of the %d times its gateway %s was restarted to match, so another deploy is changing it\nDeploy again once that deploy is done",
		ref.Name.Env, realtimeGatewayRuns, realtimeGatewayName(ref))
}

func (p *Provider) placeRealtimeGateway(ctx context.Context, ref provider.StackRef, keys map[string]string, progress progress.Log) error {
	if len(keys) == 0 {
		progress.Say("Removing the realtime gateway " + realtimeGatewayName(ref) + ": " + ref.Name.Env + " declares no realtime any more")
		return p.host.RemoveResource(ctx, host.ResourceRef{Tier: ref.Tier, Project: ref.Project, Resource: realtimeGatewayResource, Name: realtimeGatewayName(ref)})
	}
	gateway, err := newRealtimeGateway(ref, keys)
	if err != nil {
		return err
	}
	progress.Say("Serving realtime " + strings.Join(slices.Sorted(maps.Keys(keys)), ", ") + " from the gateway in container " + gateway.Name)
	return p.host.RunResource(ctx, gateway, "")
}

func (p *Provider) RemoveRealtime(ctx context.Context, ref provider.StackRef, binding provider.Binding, progress progress.Log) error {
	namespace := chooseDeclaredName(binding.Resource, binding.Name)
	if err := records.Forget(ctx, p.keyValues, boxapi.RealtimeNamespaceKey(ref.Tier, ref.Project, ref.Name.Env, namespace)); err != nil {
		return err
	}
	if err := p.runRealtimeGateway(ctx, ref, progress); err != nil {
		return err
	}
	return p.host.ForgetKept(ctx, ref.Tier, []string{nameSigningKey(ref, binding.Name)})
}
