package vps

import (
	"cmp"
	"context"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/kvstore"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
)

const (
	kvKind       = "kv"
	kvSecretName = "kv-password"
	kvReadyTries = 120
)

func kvAuthenticateCommand() []string {
	probe := strings.Join(kvstore.ValkeyReadyProbe(), " ")
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
	valkey  kvstore.Valkey
}

func readKVStore(in resources.ProvisionRequest) (kvStore, error) {
	spec := provider.KVSpec{}
	if in.Resource.KV != nil {
		spec = *in.Resource.KV
	}
	if spec.MemoryBytes == 0 {
		spec.MemoryBytes, _ = kvstore.ParseMemory(kvstore.DefaultMemory)
	}
	version := cmp.Or(spec.Version, kvstore.DefaultVersion)
	image, pinned := images.Valkey(version)
	if !pinned {
		return kvStore{}, refusal.Refuse(refusal.CodeInvalid,
			"kv %s asks for version %q; supported: %s",
			in.Resource.Name, version, strings.Join(images.ValkeyVersions(), ", "))
	}
	return kvStore{version: version, image: image, valkey: kvstore.Valkey{MemoryBytes: spec.MemoryBytes, Eviction: spec.Eviction}}, nil
}

func kvContainer(in resources.ProvisionRequest, store kvStore) host.ResourceContainer {
	return host.ResourceContainer{
		Name:     host.ResourceName(in.Ref.Project, in.Ref.Name.String(), in.Resource.Name, kvKind),
		Project:  in.Ref.Project,
		Resource: in.Resource.Name,
		Tier:     in.Ref.Tier,

		Image: store.image,
		Args:  store.valkey.Args(),
		User:  kvstore.ValkeyRunsAs,
		Env:   map[string]string{},

		Volume: host.Volume{Path: kvstore.ValkeyData, Generation: store.version, CopyOnUpgrade: true},
		Credential: host.Credential{
			Reassert: func(secret string) ([]string, string) { return kvAuthenticateCommand(), secret + "\n" },
		},
		Ready:  []string{"valkey-cli", "ping"},
		Backup: host.BackupVolume,
	}
}

func (p *Provider) ProvisionKV(ctx context.Context, in resources.ProvisionRequest, progress progress.Log) (provider.Binding, error) {
	store, err := readKVStore(in)
	if err != nil {
		return provider.Binding{}, err
	}
	name := host.ResourceName(in.Ref.Project, in.Ref.Name.String(), in.Resource.Name, kvKind)
	if store.valkey.Password, err = p.kvSecret(ctx, in, name); err != nil {
		return provider.Binding{}, err
	}
	spec, err := p.reshaped(ctx, in, transformTypeKV, kvContainer(in, store))
	if err != nil {
		return provider.Binding{}, err
	}
	if progress != nil {
		progress.Say("Provisioning kv " + in.Resource.Name + " in container " + spec.Name)
	}
	if err := p.host.RunResource(ctx, spec, store.valkey.Password); err != nil {
		return provider.Binding{}, err
	}
	return provider.Binding{
		Type:     provider.BindingKV,
		Name:     in.Resource.Name,
		Resource: in.Resource.Declared,
		Properties: map[string]string{
			provider.PropertyHost:     spec.Name,
			provider.PropertyPort:     strconv.Itoa(kvstore.ValkeyPort),
			provider.PropertyUsername: kvstore.ValkeyUsername,
			provider.PropertyPassword: store.valkey.Password,
			provider.PropertyTLS:      "false",
		},
	}, nil
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
