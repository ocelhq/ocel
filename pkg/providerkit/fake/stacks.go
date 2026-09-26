package fake

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/ocelhq/ocel/pkg/providerkit/appbuild"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/resources"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Stacks struct {
	Grants []provider.Grant

	artifacts provider.ArtifactStore
	journal   *Journal
	refusal   error

	mu          sync.Mutex
	stacks      map[string]provider.StackResult
	provisioned []provider.StackSpec
	taken       []string
	entered     func(provider.StackSpec) error
}

func NewStacks(artifacts provider.ArtifactStore) *Stacks {
	return &Stacks{artifacts: artifacts, stacks: map[string]provider.StackResult{}}
}

func (r *Stacks) journalling(journal *Journal) *Stacks {
	r.journal = journal
	return r
}

func (r *Stacks) Entering(hook func(provider.StackSpec) error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entered = hook
}

func (r *Stacks) Provisioned() []provider.StackSpec {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.provisioned)
}

func (r *Stacks) Plan(ctx context.Context, spec provider.StackSpec, _ edge.Progress) (provider.Plan, error) {
	return resources.SynthesizedPlan(ctx, r.artifacts, spec, r.Inspect(spec.Ref).Result)
}

func (r *Stacks) PlanDestroy(_ context.Context, ref provider.StackRef, _ edge.Progress) (provider.Plan, error) {
	return resources.SynthesizedRemoval(ref, r.Inspect(ref).Result), nil
}

func (r *Stacks) Provision(ctx context.Context, spec provider.StackSpec, progress edge.Progress) (provider.StackResult, error) {
	if err := ctx.Err(); err != nil {
		return provider.StackResult{}, err
	}
	r.mu.Lock()
	entered := r.entered
	r.mu.Unlock()
	if entered != nil {
		if err := entered(spec); err != nil {
			return provider.StackResult{}, err
		}
	}
	if err := spec.Images.PushMissing(ctx, progress); err != nil {
		return provider.StackResult{}, err
	}
	if err := resources.ShipUploads(ctx, r.artifacts, spec.Uploads, progress); err != nil {
		return provider.StackResult{}, err
	}
	result := provider.StackResult{}
	for _, resource := range spec.Resources {
		if resource.Binding != "" {
			continue
		}
		result.Bindings = append(result.Bindings, provider.Binding{
			Type:       resource.Type,
			Name:       resource.Name,
			Properties: propertiesFor(resource.Type, resource.Name),
			Grants:     r.Grants,
		})
	}
	result.Functions = ProvisionedFunctions(spec)
	result.Containers = ProvisionedContainers(spec)
	if spec.App != nil {
		result.EdgeBundleKey = deliveredEdgeBundle(spec)
		if spec.App.ISR != nil {
			result.ISRWriteSecret = "isr-" + spec.Ref.Name.String()
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.provisioned = append(r.provisioned, spec)
	r.stacks[stackKey(spec.Ref)] = result
	if progress != nil {
		progress.Say("provisioned " + spec.Ref.Name.String())
	}
	return result, nil
}

func (r *Stacks) RefuseNextDestroy(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refusal = err
}

func (r *Stacks) Destroy(_ context.Context, ref provider.StackRef, progress edge.Progress) error {
	r.journal.note("destroy " + ref.Name.String())
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.refusal != nil {
		refused := r.refusal
		r.refusal = nil
		return refused
	}
	delete(r.stacks, stackKey(ref))
	if progress != nil {
		progress.Say("destroyed " + ref.Name.String())
	}
	return nil
}

func (r *Stacks) Inspect(ref provider.StackRef) provider.InspectedStack {
	r.mu.Lock()
	defer r.mu.Unlock()
	result, present := r.stacks[stackKey(ref)]
	return provider.InspectedStack{Present: present, Result: result}
}

func deliveredEdgeBundle(spec provider.StackSpec) string {
	root := appbuild.AppArtifactRoot(appbuild.ArtifactRoot(), spec.App.App)
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(edge.AppBundleFile))); err != nil {
		return ""
	}
	return spec.Ref.Name.String() + "/edge/bundle.json"
}

func ProvisionedFunctions(spec provider.StackSpec) []provider.Function {
	if spec.App == nil {
		return nil
	}
	standing := make([]provider.Function, 0, len(spec.App.Functions))
	for _, function := range spec.App.Functions {
		physical := spec.Ref.Name.String() + "-" + function.Name
		standing = append(standing, provider.Function{
			Name:     function.Name,
			Physical: physical,
			URL:      "https://" + physical + ".fn.fake.invalid",
		})
	}
	if len(standing) == 0 {
		return nil
	}
	return standing
}

func ProvisionedContainers(spec provider.StackSpec) []provider.AppContainer {
	if spec.App == nil || spec.App.Compute != provider.ComputeContainer {
		return nil
	}
	physical := spec.Ref.Name.String() + "-" + spec.App.App
	return []provider.AppContainer{{
		Name:     spec.App.App,
		Physical: physical,
		URL:      "https://" + physical + ".ctr.fake.invalid",
		Image:    spec.App.Image,
	}}
}

func (r *Stacks) recordDestroyed(names ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.taken = append(r.taken, names...)
}

func (r *Stacks) Destroyed() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.taken)
}

func stackKey(ref provider.StackRef) string {
	return ref.Project + "/" + string(ref.Class) + "/" + ref.Name.String()
}

func propertiesFor(t provider.BindingType, name string) map[string]string {
	properties := map[string]string{}
	for _, property := range provider.RequiredProperties(t) {
		switch property {
		case provider.PropertyPort:
			properties[property] = "5432"
		case provider.PropertyBucket:
			properties[property] = name + "-fake"
		default:
			properties[property] = "fake-" + property
		}
	}
	return properties
}

func (*Provider) ProvisionFunctions(_ context.Context, spec provider.StackSpec, _ edge.Progress) ([]provider.Function, error) {
	return ProvisionedFunctions(spec), nil
}

func (p *Provider) RemoveFunctions(_ context.Context, _ provider.StackRef, functions []provider.Function, _ edge.Progress) error {
	for _, function := range functions {
		p.stacks.recordDestroyed(function.Name)
	}
	return nil
}

func (*Provider) ProvisionContainers(_ context.Context, spec provider.StackSpec, _ edge.Progress) ([]provider.AppContainer, error) {
	return ProvisionedContainers(spec), nil
}

func (p *Provider) RemoveContainers(_ context.Context, _ provider.StackRef, containers []provider.AppContainer, _ edge.Progress) error {
	for _, container := range containers {
		p.stacks.recordDestroyed(container.Name)
	}
	return nil
}
