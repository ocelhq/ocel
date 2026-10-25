package kv

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
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/component"
	"github.com/ocelhq/ocel/platform/vps/provider/resources/container"
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

var shape = component.Shape{Kind: "kv", OwnEnv: []string{"VALKEY_EXTRA_FLAGS"}, RefuseOwnedArgs: valkey.RefuseOwnedArgs}

type Resource struct{ box component.Box }

func New(box component.Box) Resource { return Resource{box: box} }

type kvStore struct {
	name    string
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
	name, err := container.Name(in.Ref, in.Resource.Name, kvKind)
	if err != nil {
		return kvStore{}, err
	}
	return kvStore{name: name, version: version, image: image, valkey: valkey.Server{MemoryBytes: spec.MemoryBytes, Eviction: spec.Eviction}}, nil
}

func kvContainer(in resources.ProvisionRequest, store kvStore) container.Spec {
	return container.Spec{
		Name:     store.name,
		Project:  in.Ref.Project,
		Resource: in.Resource.Name,
		Tier:     in.Ref.Tier,

		Image:  store.image,
		Args:   store.valkey.Args(),
		User:   valkey.RunsAs,
		Env:    map[string]string{},
		Memory: strconv.FormatInt(2*store.valkey.MemoryBytes+kvOverheadBytes, 10),

		Volume: container.Volume{Path: valkey.DataDir, Generation: store.version, CopyOnUpgrade: true},
		Credential: container.Credential{
			Reassert: func(secret string) ([]string, string) { return kvAuthenticateCommand(), secret + "\n" },
		},
		Ready:  []string{"valkey-cli", "ping"},
		Backup: boxapi.BackupKV,
	}
}

func ResolveContainer(in resources.ProvisionRequest) (container.Spec, error) {
	store, err := readKVStore(in)
	if err != nil {
		return container.Spec{}, err
	}
	return kvContainer(in, store), nil
}

func newKVBinding(in resources.ProvisionRequest, spec container.Spec, password string) provider.Binding {
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

func (r Resource) Plan(ctx context.Context, in resources.ProvisionRequest, recorded *provider.Binding) (provider.ChangeAction, string, error) {
	store, err := readKVStore(in)
	if err != nil {
		return "", "", err
	}
	bound, err := newKVSecretAssociatedData(in.Ref, in.Resource.Name)
	if err != nil {
		return "", "", err
	}
	sealed, password, err := r.box.ReadKeptSealed(ctx, in.Ref.Tier, store.name, in.Resource.Name, bound)
	if err != nil {
		return "", "", err
	}
	store.valkey.Password = password
	spec, err := r.box.Reshaped(ctx, in, shape, kvContainer(in, store))
	if err != nil {
		return "", "", err
	}
	action, reason, err := r.box.Containers.Plan(ctx, spec)
	if err != nil || action == provider.ActionCreate {
		return action, reason, err
	}
	if len(sealed) == 0 {
		return provider.ActionReplace, "the box keeps no password for it, so a new one is minted and its container " + spec.Name + " replaced to take it", nil
	}
	var change component.Change
	change.Add(action, reason)
	change.AddRecorded(recorded, newKVBinding(in, spec, ""))
	return change.Report()
}

func (r Resource) Provision(ctx context.Context, in resources.ProvisionRequest, progress progress.Log) (provider.Binding, error) {
	store, err := readKVStore(in)
	if err != nil {
		return provider.Binding{}, err
	}
	if store.valkey.Password, err = r.secret(ctx, in, store.name); err != nil {
		return provider.Binding{}, err
	}
	spec, err := r.box.Reshaped(ctx, in, shape, kvContainer(in, store))
	if err != nil {
		return provider.Binding{}, err
	}
	progress.Say("Provisioning kv " + in.Resource.Name)
	if err := r.box.Containers.Run(ctx, spec, store.valkey.Password); err != nil {
		return provider.Binding{}, err
	}
	return newKVBinding(in, spec, store.valkey.Password), nil
}

func (r Resource) Remove(ctx context.Context, ref provider.StackRef, binding provider.Binding, progress progress.Log) error {
	name, err := container.Name(ref, binding.Name, kvKind)
	if err != nil {
		return err
	}
	progress.Say("Removing kv " + binding.Name + " and its data")
	return r.box.Containers.Remove(ctx, container.Ref{Tier: ref.Tier, Project: ref.Project, Resource: binding.Name, Name: name})
}

func newKVSecretAssociatedData(ref provider.StackRef, resource string) (seal.AssociatedData, error) {
	return boxapi.NewSecretAssociatedData(ref.Project, ref.Tier, ref.Name.String(), component.SecretFolder, resource, kvSecretName)
}

func (r Resource) secret(ctx context.Context, in resources.ProvisionRequest, name string) (string, error) {
	bound, err := newKVSecretAssociatedData(in.Ref, in.Resource.Name)
	if err != nil {
		return "", err
	}
	return r.box.KeptSecret(ctx, in, name, bound)
}
