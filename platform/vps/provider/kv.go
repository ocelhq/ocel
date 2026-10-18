package vps

import (
	"cmp"
	"context"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/pkg/valkey"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
)

const (
	kvKind       = "kv"
	kvSecretName = "kv-password"
	kvReadyTries = 120

	kvOverheadBytes = 64 << 20
)

func kvAuthenticateCommand() []string {
	probe := strings.Join(valkey.ReadyProbe(), " ")
	return []string{"sh", "-c",
		"IFS= read -r password\n" +
			"tries=0\n" +
			"until printf '%s' \"$password\" | " + probe + "; do\n" +
			"tries=$((tries+1))\n" +
			"[ \"$tries\" -lt " + strconv.Itoa(kvReadyTries) + " ] || exit 1\n" +
			"sleep 1\n" +
			"done"}
}

type kvStore struct {
	version string
	image   string
	valkey  valkey.Server
}

func readKVStore(in resources.ProvisionRequest) (kvStore, error) {
	spec := provider.KVSpec{}
	if in.Resource.KV != nil {
		spec = *in.Resource.KV
	}
	if spec.MemoryBytes == 0 {
		spec.MemoryBytes, _ = valkey.ParseMemory(valkey.DefaultMemory)
	}
	version := cmp.Or(spec.Version, valkey.DefaultVersion)
	image, pinned := images.Valkey(version)
	if !pinned {
		return kvStore{}, refusal.Refuse(refusal.CodeInvalid,
			"kv %s asks for version %q; supported: %s",
			in.Resource.Name, version, strings.Join(images.ValkeyVersions(), ", "))
	}
	return kvStore{version: version, image: image, valkey: valkey.Server{MemoryBytes: spec.MemoryBytes, Eviction: spec.Eviction}}, nil
}

func nameKV(ref provider.StackRef, resource string) string {
	return host.ResourceName(ref.Project, ref.Name.String(), resource, kvKind)
}

func kvContainer(in resources.ProvisionRequest, store kvStore) host.ResourceContainer {
	return host.ResourceContainer{
		Name:     nameKV(in.Ref, in.Resource.Name),
		Project:  in.Ref.Project,
		Resource: in.Resource.Name,
		Tier:     in.Ref.Tier,

		Image:  store.image,
		Args:   store.valkey.Args(),
		User:   valkey.RunsAs,
		Env:    map[string]string{},
		Memory: strconv.FormatInt(2*store.valkey.MemoryBytes+kvOverheadBytes, 10),

		Volume: host.Volume{Path: valkey.DataDir, Generation: store.version, CopyOnUpgrade: true},
		Credential: host.Credential{
			Reassert: func(secret string) ([]string, string) { return kvAuthenticateCommand(), secret + "\n" },
		},
		Ready:  []string{"valkey-cli", "ping"},
		Backup: host.BackupKV,
	}
}

func newKVBinding(in resources.ProvisionRequest, spec host.ResourceContainer, password string) provider.Binding {
	return provider.Binding{
		Type:     provider.BindingKV,
		Name:     in.Resource.Name,
		Resource: in.Resource.Declared,
		Properties: map[string]string{
			provider.PropertyHost:     spec.Name,
			provider.PropertyPort:     strconv.Itoa(valkey.Port),
			provider.PropertyUsername: valkey.Username,
			provider.PropertyPassword: password,
			provider.PropertyTLS:      "false",
		},
	}
}

func (p *Provider) PlanKV(ctx context.Context, in resources.ProvisionRequest, recorded *provider.Binding) (provider.ChangeAction, string, error) {
	store, err := readKVStore(in)
	if err != nil {
		return "", "", err
	}
	bound, err := newKVSecretAssociatedData(in.Ref, in.Resource.Name)
	if err != nil {
		return "", "", err
	}
	sealed, password, err := p.readKeptSealed(ctx, in.Ref.Tier, nameKV(in.Ref, in.Resource.Name), in.Resource.Name, bound)
	if err != nil {
		return "", "", err
	}
	store.valkey.Password = password
	spec, err := p.reshaped(ctx, in, transformTypeKV, kvContainer(in, store))
	if err != nil {
		return "", "", err
	}
	action, reason, err := p.host.PlanResource(ctx, spec)
	if err != nil || action == provider.ActionCreate {
		return action, reason, err
	}
	if len(sealed) == 0 {
		return provider.ActionReplace, "the box keeps no password for it, so a new one is minted and its container " + spec.Name + " replaced to take it", nil
	}
	var change resourceChange
	change.add(action, reason)
	change.addRecorded(recorded, newKVBinding(in, spec, ""))
	return change.report()
}

func (p *Provider) ProvisionKV(ctx context.Context, in resources.ProvisionRequest, progress progress.Log) (provider.Binding, error) {
	store, err := readKVStore(in)
	if err != nil {
		return provider.Binding{}, err
	}
	if store.valkey.Password, err = p.kvSecret(ctx, in, nameKV(in.Ref, in.Resource.Name)); err != nil {
		return provider.Binding{}, err
	}
	spec, err := p.reshaped(ctx, in, transformTypeKV, kvContainer(in, store))
	if err != nil {
		return provider.Binding{}, err
	}
	progress.Say("Provisioning kv " + in.Resource.Name + " in container " + spec.Name)
	if err := p.host.RunResource(ctx, spec, store.valkey.Password); err != nil {
		return provider.Binding{}, err
	}
	return newKVBinding(in, spec, store.valkey.Password), nil
}

func (p *Provider) RemoveKV(ctx context.Context, ref provider.StackRef, binding provider.Binding, progress progress.Log) error {
	name := nameKV(ref, binding.Name)
	progress.Say("Removing kv " + binding.Name + ", its container " + name + " and its data")
	return p.host.RemoveResource(ctx, host.ResourceRef{Tier: ref.Tier, Project: ref.Project, Resource: binding.Name, Name: name})
}

func newKVSecretAssociatedData(ref provider.StackRef, resource string) (seal.AssociatedData, error) {
	return live.NewSecretAssociatedData(ref.Project, ref.Tier, ref.Name.String(), resourceSecretFolder, resource, kvSecretName)
}

func (p *Provider) kvSecret(ctx context.Context, in resources.ProvisionRequest, name string) (string, error) {
	bound, err := newKVSecretAssociatedData(in.Ref, in.Resource.Name)
	if err != nil {
		return "", err
	}
	return p.keptSecret(ctx, in, name, bound)
}
