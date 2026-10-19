package vps

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"google.golang.org/protobuf/proto"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
	"github.com/ocelhq/ocel/platform/vps/provider/resources/bucket"
	"github.com/ocelhq/ocel/platform/vps/provider/resources/container"
	"github.com/ocelhq/ocel/platform/vps/provider/resources/kv"
	"github.com/ocelhq/ocel/platform/vps/provider/resources/postgres"
	"github.com/ocelhq/ocel/platform/vps/provider/resources/realtime"
	"github.com/ocelhq/ocel/platform/vps/provider/resources/topic"
)

func (p *Provider) ListResources(ctx context.Context, ref provider.StackRef, recorded provider.StackResult) ([]provider.PhysicalResource, error) {
	listed := physicalResources{location: p.options.SSH.session().Destination()}
	for _, app := range recorded.Containers {
		listed.add(machine.KindContainer, app.Physical)
	}
	if len(recorded.Containers) > 0 {
		workers, err := topic.NewWorkers(p.components()).ListContainers(ctx, ref)
		if err != nil {
			return nil, err
		}
		for _, worker := range workers {
			listed.add(machine.KindContainer, worker)
		}
	}
	declared, err := p.readDeclaredResources(ctx, ref, recorded.Bindings)
	if err != nil {
		return nil, err
	}
	for _, binding := range recorded.Bindings {
		spec, err := resolveBindingContainer(ref, binding, declared[binding.Name])
		if err != nil {
			return nil, err
		}
		listed.addContainer(spec)
	}
	return listed.resources, nil
}

type physicalResources struct {
	location  string
	resources []provider.PhysicalResource
}

func (l *physicalResources) add(kind, id string) {
	if id == "" {
		return
	}
	resource := provider.PhysicalResource{Kind: kind, ID: id, Location: l.location}
	if !slices.Contains(l.resources, resource) {
		l.resources = append(l.resources, resource)
	}
}

func (l *physicalResources) addContainer(spec container.Spec) {
	l.add(machine.KindContainer, spec.Name)
	for _, volume := range spec.VolumeNames() {
		l.add(machine.KindVolume, volume)
	}
}

func isVersioned(binding provider.Binding) bool {
	return binding.Type == provider.BindingPostgres || binding.Type == provider.BindingKV
}

func (p *Provider) readDeclaredResources(ctx context.Context, ref provider.StackRef, bindings []provider.Binding) (map[string]*contractv1.ManifestResource, error) {
	if !slices.ContainsFunc(bindings, isVersioned) {
		return nil, nil
	}
	recorded, _, err := stackrecords.Read(ctx, p.keyValues, ref.Tier, ref.Project, ref.Name)
	if err != nil {
		return nil, err
	}
	var manifest contractv1.Manifest
	if err := proto.Unmarshal(recorded.Resources, &manifest); err != nil {
		return nil, fmt.Errorf("read what %s declares: %w", ref.Name, err)
	}
	declared := map[string]*contractv1.ManifestResource{}
	for _, resource := range manifest.GetResources() {
		declared[cmp.Or(resource.GetLogicalName(), resource.GetResource().GetName())] = resource
	}
	return declared, nil
}

func resolveBindingContainer(ref provider.StackRef, binding provider.Binding, declared *contractv1.ManifestResource) (container.Spec, error) {
	in := resources.ProvisionRequest{Ref: ref, Resource: provider.Resource{Name: binding.Name, Declared: binding.Resource, Type: binding.Type}}
	switch binding.Type {
	case provider.BindingPostgres:
		in.Resource.Postgres = &provider.PostgresSpec{Version: declared.GetPostgres().GetVersion()}
		return postgres.ResolveContainer(in)
	case provider.BindingKV:
		in.Resource.KV = &provider.KVSpec{Version: declared.GetKv().GetVersion()}
		return kv.ResolveContainer(in)
	case provider.BindingBucket:
		return bucket.NewStoreContainer(ref)
	case provider.BindingTopic, provider.BindingTask:
		return topic.NewQueueContainer(ref)
	case provider.BindingRealtime:
		name, err := realtime.GatewayName(ref)
		if err != nil {
			return container.Spec{}, err
		}
		return realtime.NewGateway(ref, name, nil)
	}
	return container.Spec{}, refusal.Refuse(refusal.CodeNotReady,
		"%s records %s %s, and a box lists no resources for a %s", ref.Name, binding.Type, binding.Name, binding.Type)
}
