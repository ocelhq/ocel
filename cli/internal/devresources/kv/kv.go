package kv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/devresources/binding"
	"github.com/ocelhq/ocel/cli/internal/devresources/docker"
	"github.com/ocelhq/ocel/cli/internal/devresources/secret"
	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/kvstore"
	"github.com/ocelhq/ocel/pkg/naming"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
)

const (
	backend = "kv"
	readyIn = 2 * time.Minute
)

type store struct {
	container docker.Container
	name      string
	args      []string
	password  string
	version   string
	ready     bool
}

type Backend struct {
	open       docker.OpenFunc
	secretsDir string

	mu     sync.Mutex
	stores map[string]*store
}

func New(open docker.OpenFunc, secretsDir string) *Backend {
	return &Backend{open: open, secretsDir: secretsDir, stores: map[string]*store{}}
}

func (b *Backend) Resolve(ctx context.Context, project string, resources []declaration.Resource) ([]binding.Resolved, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	wanted := make([]kvstore.Settings, 0, len(resources))
	for _, resource := range resources {
		one, err := kvstore.ReadSettings(resource.KV)
		if err != nil {
			return nil, fmt.Errorf("kv %q: %w", resource.Name, err)
		}
		wanted = append(wanted, one)
	}

	out := make([]binding.Resolved, 0, len(resources))
	for i, resource := range resources {
		engine, err := b.open(ctx)
		if err != nil {
			return nil, err
		}
		running, err := b.runStore(ctx, engine, project, resource.Name, wanted[i])
		if err != nil {
			return nil, err
		}
		bound, err := bind(resource, running)
		if err != nil {
			return nil, err
		}
		bound.Origin = fmt.Sprintf("valkey:%s @ %s", running.version, running.container.Address)
		out = append(out, bound)
	}
	return out, nil
}

func storeKey(name string) string {
	sum := sha256.Sum256([]byte(name))
	return naming.Sanitize(name) + "-" + hex.EncodeToString(sum[:4])
}

func (b *Backend) runStore(ctx context.Context, engine docker.Engine, project, name string, wanted kvstore.Settings) (*store, error) {
	password, err := secret.Ensure(filepath.Join(b.secretsDir, "kv-"+storeKey(name)+"-password"),
		secret.Purpose{Owner: fmt.Sprintf("kv %q", name), Noun: "password"})
	if err != nil {
		return nil, err
	}
	valkey := kvstore.Valkey{Password: password, MemoryBytes: wanted.MemoryBytes, Eviction: wanted.Eviction}
	image, _ := images.Valkey(wanted.Version)
	containerName := docker.Name(project, backend, storeKey(name), wanted.Version)
	spec := docker.Spec{
		Name:       containerName,
		Image:      image,
		Args:       valkey.Args(),
		User:       kvstore.ValkeyRunsAs,
		Port:       kvstore.ValkeyPort,
		Volume:     containerName,
		VolumePath: kvstore.ValkeyData,
		Labels:     docker.Labels(project, backend),
	}
	running, known := b.stores[name]
	if !known || running.name != spec.Name || !slices.Equal(running.args, spec.Args) {
		container, err := engine.Run(ctx, spec)
		if err != nil {
			return nil, err
		}
		if known && running.name != spec.Name {
			if err := engine.Stop(ctx, running.container.ID); err != nil {
				return nil, err
			}
		}
		running = &store{container: container, name: spec.Name, args: spec.Args, password: password, version: wanted.Version}
		b.stores[name] = running
	}
	container := running.container
	if !running.ready {
		err := docker.WaitReady(ctx, readyIn, func(ctx context.Context) error {
			_, err := engine.ExecInput(ctx, container.ID, password, kvstore.ValkeyReadyProbe()...)
			return err
		})
		if err != nil {
			return nil, fmt.Errorf("kv %q never accepted its user: %w", name, err)
		}
		running.ready = true
	}
	return running, nil
}

func bind(resource declaration.Resource, running *store) (binding.Resolved, error) {
	host, rawPort, err := net.SplitHostPort(running.container.Address)
	if err != nil {
		return binding.Resolved{}, err
	}
	port, err := strconv.ParseInt(rawPort, 10, 32)
	if err != nil {
		return binding.Resolved{}, err
	}
	return binding.Encode(resource.Type, &bindingsv1.Binding{
		Name: resource.Name,
		Properties: &bindingsv1.Binding_Kv{Kv: &bindingsv1.KvProperties{
			Host: host, Port: int32(port), Username: kvstore.ValkeyUsername, Password: running.password,
		}},
	})
}

func (b *Backend) Close(ctx context.Context, stopContainers bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	stores := b.stores
	b.stores = map[string]*store{}
	if !stopContainers || len(stores) == 0 {
		return nil
	}
	engine, err := b.open(ctx)
	if err != nil {
		return err
	}
	stopping := make(chan error, len(stores))
	for _, one := range stores {
		go func() { stopping <- engine.Stop(ctx, one.container.ID) }()
	}
	var failed []error
	for range stores {
		if err := <-stopping; err != nil {
			failed = append(failed, err)
		}
	}
	return errors.Join(failed...)
}
