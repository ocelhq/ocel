package fake

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/appbuild"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Stacks struct {
	Grants []providerkit.Grant

	artifacts providerkit.ArtifactStore
	journal   *Journal
	refusal   error

	mu      sync.Mutex
	stacks  map[string]providerkit.StackResult
	plans   []providerkit.StackPlan
	taken   []string
	entered func(providerkit.StackPlan) error
}

func NewStacks(artifacts providerkit.ArtifactStore) *Stacks {
	return &Stacks{artifacts: artifacts, stacks: map[string]providerkit.StackResult{}}
}

func (r *Stacks) journalling(journal *Journal) *Stacks {
	r.journal = journal
	return r
}

func (r *Stacks) Entering(hook func(providerkit.StackPlan) error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entered = hook
}

func (r *Stacks) Plans() []providerkit.StackPlan {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.plans)
}

func (r *Stacks) Plan(ctx context.Context, plan providerkit.StackPlan, _ edge.Progress) (providerkit.Plan, error) {
	return providerkit.SynthesizedPlan(ctx, r.artifacts, plan, r.State(plan.Ref).Result)
}

func (r *Stacks) PlanDestroy(_ context.Context, ref providerkit.StackRef, _ edge.Progress) (providerkit.Plan, error) {
	return providerkit.SynthesizedRemoval(ref, r.State(ref).Result), nil
}

func (r *Stacks) Provision(ctx context.Context, plan providerkit.StackPlan, progress edge.Progress) (providerkit.StackResult, error) {
	if err := ctx.Err(); err != nil {
		return providerkit.StackResult{}, err
	}
	r.mu.Lock()
	entered := r.entered
	r.mu.Unlock()
	if entered != nil {
		if err := entered(plan); err != nil {
			return providerkit.StackResult{}, err
		}
	}
	if err := plan.Images.Ship(ctx, progress); err != nil {
		return providerkit.StackResult{}, err
	}
	if err := providerkit.ShipUploads(ctx, r.artifacts, plan.Uploads, progress); err != nil {
		return providerkit.StackResult{}, err
	}
	result := providerkit.StackResult{}
	for _, resource := range plan.Resources {
		if resource.Binding != "" {
			continue
		}
		result.Bindings = append(result.Bindings, providerkit.Binding{
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

func (r *Stacks) Destroy(_ context.Context, ref providerkit.StackRef, progress edge.Progress) error {
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

func (r *Stacks) State(ref providerkit.StackRef) providerkit.StackState {
	r.mu.Lock()
	defer r.mu.Unlock()
	result, present := r.stacks[stackKey(ref)]
	return providerkit.StackState{Present: present, Result: result}
}

func deliveredEdgeBundle(plan providerkit.StackPlan) string {
	root := appbuild.AppArtifactRoot(appbuild.ArtifactRoot(), plan.App.App)
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(edge.AppBundleFile))); err != nil {
		return ""
	}
	return plan.Ref.Name.String() + "/edge/bundle.json"
}

func StoodUpFunctions(plan providerkit.StackPlan) []providerkit.Function {
	if plan.App == nil {
		return nil
	}
	standing := make([]providerkit.Function, 0, len(plan.App.Functions))
	for _, function := range plan.App.Functions {
		physical := plan.Ref.Name.String() + "-" + function.Name
		standing = append(standing, providerkit.Function{
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

func StoodUpContainers(plan providerkit.StackPlan) []providerkit.AppContainer {
	if plan.App == nil || plan.App.Compute != providerkit.ComputeContainer {
		return nil
	}
	physical := plan.Ref.Name.String() + "-" + plan.App.App
	return []providerkit.AppContainer{{
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

func stackKey(ref providerkit.StackRef) string {
	return ref.Project + "/" + string(ref.Class) + "/" + ref.Name.String()
}

func propertiesFor(t providerkit.BindingType, name string) map[string]string {
	properties := map[string]string{}
	for _, property := range providerkit.RequiredProperties(t) {
		switch property {
		case providerkit.PropertyPort:
			properties[property] = "5432"
		case providerkit.PropertyBucket:
			properties[property] = name + "-fake"
		default:
			properties[property] = "fake-" + property
		}
	}
	return properties
}

func (*Provider) ProvisionFunctions(_ context.Context, plan providerkit.StackPlan, _ edge.Progress) ([]providerkit.Function, error) {
	return StoodUpFunctions(plan), nil
}

func (p *Provider) RemoveFunctions(_ context.Context, _ providerkit.StackRef, functions []providerkit.Function, _ edge.Progress) error {
	for _, function := range functions {
		p.stacks.tookDown(function.Name)
	}
	return nil
}

func (*Provider) ProvisionContainers(_ context.Context, plan providerkit.StackPlan, _ edge.Progress) ([]providerkit.AppContainer, error) {
	return StoodUpContainers(plan), nil
}

func (p *Provider) RemoveContainers(_ context.Context, _ providerkit.StackRef, containers []providerkit.AppContainer, _ edge.Progress) error {
	for _, container := range containers {
		p.stacks.tookDown(container.Name)
	}
	return nil
}
