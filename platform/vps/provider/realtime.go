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
	"strings"

	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/progress"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/realtime/gatewayenv"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
)

const (
	realtimeGatewayResource = "ocel-realtime"
	realtimeGatewayOwner    = "ocel"
	realtimeGatewayKind     = "realtime"
	realtimeGatewayUser     = "65532:65532"
	realtimeGatewayRuns     = 3
	realtimeSigningKind     = "signing-key"
	realtimeSigningName     = "signing-key"
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
		Mounts:   []host.Mount{{Source: host.RealtimeDir, Target: host.RealtimeMount}},
		Labels:   map[string]string{host.LabelBinary: host.RealtimeBinarySum()},
		Env: map[string]string{
			gatewayenv.HostVar: realtimeGatewayAddress(ref),
			gatewayenv.KeysVar: string(encoded),
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

func realtimeSigningKept(ref provider.StackRef, binding string) string {
	return host.ResourceName(ref.Project, ref.Name.String(), binding, realtimeSigningKind)
}

func (p *Provider) ProvisionRealtime(ctx context.Context, in resources.ProvisionRequest, progress progress.Log) (provider.Binding, error) {
	if in.Resource.Realtime == nil {
		return provider.Binding{}, refusal.Refuse(refusal.CodeInvalid,
			"realtime %s reached the box with no config, so nothing knows its channels", in.Resource.Declared)
	}
	seed, err := p.realtimeSigningSeed(ctx, in)
	if err != nil {
		return provider.Binding{}, err
	}
	verifyKey := base64.StdEncoding.EncodeToString(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey))
	namespace := chooseDeclaredName(in.Resource.Declared, in.Resource.Name)
	record, err := json.Marshal(live.RealtimeNamespace{VerifyKey: verifyKey})
	if err != nil {
		return provider.Binding{}, err
	}
	key := live.RealtimeNamespaceKey(in.Ref.Tier, in.Ref.Project, in.Ref.Name.Env, namespace)
	if err := keyvalue.Change(ctx, p.keyValues, key, func(recorded keyvalue.Entry) ([]byte, bool, error) {
		return record, string(recorded.Value) != string(record), nil
	}); err != nil {
		return provider.Binding{}, err
	}
	if err := p.runRealtimeGateway(ctx, in.Ref, progress); err != nil {
		return provider.Binding{}, err
	}
	return provider.Binding{
		Type:     provider.BindingRealtime,
		Name:     in.Resource.Name,
		Resource: in.Resource.Declared,
		Properties: map[string]string{
			provider.PropertyTransport:  bindingsv1.RealtimeTransport_REALTIME_TRANSPORT_OCEL_GATEWAY.String(),
			provider.PropertyURL:        live.RealtimeSocketPath,
			provider.PropertyHost:       realtimeGatewayAddress(in.Ref),
			provider.PropertySigningKey: base64.StdEncoding.EncodeToString(seed),
			provider.PropertyVerifyKey:  verifyKey,
		},
	}, nil
}

func (p *Provider) realtimeSigningSeed(ctx context.Context, in resources.ProvisionRequest) ([]byte, error) {
	bound, err := live.NewSecretAssociatedData(in.Ref.Project, in.Ref.Tier, in.Ref.Name.String(), resourceSecretFolder, in.Resource.Name, realtimeSigningName)
	if err != nil {
		return nil, err
	}
	_, opened, err := p.keptSealed(ctx, in.Ref.Tier, realtimeSigningKept(in.Ref, in.Resource.Name), in.Resource.Name, bound, mintSigningSeed)
	if err != nil {
		return nil, err
	}
	seed, err := base64.StdEncoding.DecodeString(opened)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, refusal.Refuse(refusal.CodeNotReady,
			"the signing key kept for realtime %s is no Ed25519 seed\nRemove %s on the box",
			in.Resource.Declared, host.KeptPath(in.Ref.Tier, realtimeSigningKept(in.Ref, in.Resource.Name)))
	}
	return seed, nil
}

func (p *Provider) readRealtimeKeys(ctx context.Context, ref provider.StackRef) (map[string]string, error) {
	partition, under := live.RealtimeNamespacesUnder(ref.Tier, ref.Project, ref.Name.Env)
	entries, err := p.keyValues.List(ctx, partition, under...)
	if err != nil {
		return nil, err
	}
	keys := map[string]string{}
	for _, entry := range entries {
		if len(entry.Key.Path) != len(under)+1 {
			continue
		}
		var recorded live.RealtimeNamespace
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
		say(progress, "Removing the realtime gateway "+realtimeGatewayName(ref)+": "+ref.Name.Env+" declares no realtime any more")
		return p.host.RemoveResource(ctx, host.ResourceRef{Tier: ref.Tier, Project: ref.Project, Resource: realtimeGatewayResource, Name: realtimeGatewayName(ref)})
	}
	gateway, err := newRealtimeGateway(ref, keys)
	if err != nil {
		return err
	}
	say(progress, "Serving realtime "+strings.Join(slices.Sorted(maps.Keys(keys)), ", ")+" from the gateway in container "+gateway.Name)
	return p.host.RunResource(ctx, gateway, "")
}

func say(progress progress.Log, line string) {
	if progress != nil {
		progress.Say(line)
	}
}

func (p *Provider) removeRealtime(ctx context.Context, ref provider.StackRef, binding provider.Binding, progress progress.Log) error {
	namespace := chooseDeclaredName(binding.Resource, binding.Name)
	if err := keyvalue.Forget(ctx, p.keyValues, live.RealtimeNamespaceKey(ref.Tier, ref.Project, ref.Name.Env, namespace)); err != nil {
		return err
	}
	if err := p.runRealtimeGateway(ctx, ref, progress); err != nil {
		return err
	}
	return p.host.ForgetKept(ctx, ref.Tier, []string{realtimeSigningKept(ref, binding.Name)})
}
