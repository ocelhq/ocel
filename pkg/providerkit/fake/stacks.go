package fake

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/appbuild"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Stacks struct {
	Grants []provider.Grant

	artifacts provider.ArtifactStore
	journal   *Journal
	refusal   error

	mu      sync.Mutex
	stacks  map[string]provider.StackResult
	plans   []provider.StackPlan
	taken   []string
	entered func(provider.StackPlan) error
}

func NewStacks(artifacts provider.ArtifactStore) *Stacks {
	return &Stacks{artifacts: artifacts, stacks: map[string]provider.StackResult{}}
}

func (r *Stacks) journalling(journal *Journal) *Stacks {
	r.journal = journal
	return r
}

func (r *Stacks) Entering(hook func(provider.StackPlan) error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entered = hook
}

func (r *Stacks) Plans() []provider.StackPlan {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.plans)
}

func (r *Stacks) Plan(ctx context.Context, plan provider.StackPlan, _ edge.Progress) (provider.Plan, error) {
	return providerkit.SynthesizedPlan(ctx, r.artifacts, plan, r.State(plan.Ref).Result)
}

func (r *Stacks) PlanDestroy(_ context.Context, ref provider.StackRef, _ edge.Progress) (provider.Plan, error) {
	return providerkit.SynthesizedRemoval(ref, r.State(ref).Result), nil
}

func (r *Stacks) Provision(ctx context.Context, plan provider.StackPlan, progress edge.Progress) (provider.StackResult, error) {
	if err := ctx.Err(); err != nil {
		return provider.StackResult{}, err
	}
	r.mu.Lock()
	entered := r.entered
	r.mu.Unlock()
	if entered != nil {
		if err := entered(plan); err != nil {
			return provider.StackResult{}, err
		}
	}
	if err := plan.Images.PushMissing(ctx, progress); err != nil {
		return provider.StackResult{}, err
	}
	if err := providerkit.ShipUploads(ctx, r.artifacts, plan.Uploads, progress); err != nil {
		return provider.StackResult{}, err
	}
	result := provider.StackResult{}
	for _, resource := range plan.Resources {
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
	result.Functions = StoodUpFunctions(plan)
	result.Containers = StoodUpContainers(plan)
	if plan.App != nil {
		result.EdgeBundleKey = deliveredEdgeBundle(plan)
		if plan.App.ISR != nil {
			result.ISRWriteSecret = "isr-" + plan.Ref.Name.String()
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.plans = append(r.plans, plan)
	r.stacks[stackKey(plan.Ref)] = result
	if progress != nil {
		progress.Say("provisioned " + plan.Ref.Name.String())
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

func (r *Stacks) State(ref provider.StackRef) provider.StackState {
	r.mu.Lock()
	defer r.mu.Unlock()
	result, present := r.stacks[stackKey(ref)]
	return provider.StackState{Present: present, Result: result}
}

func deliveredEdgeBundle(plan provider.StackPlan) string {
	root := appbuild.AppArtifactRoot(appbuild.ArtifactRoot(), plan.App.App)
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(edge.AppBundleFile))); err != nil {
		return ""
	}
	return plan.Ref.Name.String() + "/edge/bundle.json"
}

func StoodUpFunctions(plan provider.StackPlan) []provider.Function {
	if plan.App == nil {
		return nil
	}
	standing := make([]provider.Function, 0, len(plan.App.Functions))
	for _, function := range plan.App.Functions {
		physical := plan.Ref.Name.String() + "-" + function.Name
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

func StoodUpContainers(plan provider.StackPlan) []provider.AppContainer {
	if plan.App == nil || plan.App.Compute != provider.ComputeContainer {
		return nil
	}
	physical := plan.Ref.Name.String() + "-" + plan.App.App
	return []provider.AppContainer{{
		Name:     plan.App.App,
		Physical: physical,
		URL:      "https://" + physical + ".ctr.fake.invalid",
		Image:    plan.App.Image,
	}}
}

func (r *Stacks) tookDown(names ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.taken = append(r.taken, names...)
}

func (r *Stacks) TakenDown() []string {
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

func (*Provider) ProvisionFunctions(_ context.Context, plan provider.StackPlan, _ edge.Progress) ([]provider.Function, error) {
	return StoodUpFunctions(plan), nil
}

func (p *Provider) RemoveFunctions(_ context.Context, _ provider.StackRef, functions []provider.Function, _ edge.Progress) error {
	for _, function := range functions {
		p.stacks.tookDown(function.Name)
	}
	return nil
}

func (*Provider) ProvisionContainers(_ context.Context, plan provider.StackPlan, _ edge.Progress) ([]provider.AppContainer, error) {
	return StoodUpContainers(plan), nil
}

func (p *Provider) RemoveContainers(_ context.Context, _ provider.StackRef, containers []provider.AppContainer, _ edge.Progress) error {
	for _, container := range containers {
		p.stacks.tookDown(container.Name)
	}
	return nil
}
