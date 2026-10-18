package fake

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/functioncode"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/refusal"
)

type Stacks struct {
	Grants         []provider.Grant
	PasswordSecret string

	artifacts provider.ArtifactStore
	journal   *Journal
	refusal   error

	mu             sync.Mutex
	stacks         map[string]provider.StackResult
	provisioned    []provider.StackSpec
	taken          []string
	destroyedWith  []provider.ImageStore
	leasedDestroys []string
	entered        func(provider.StackSpec) error
	destroying     func(provider.StackRef) error
	teardown       func(provider.StackRef) provider.Plan

	shipped              map[string][]functioncode.Upload
	images               *functioncode.ImageSource
	routeTables          map[string][]byte
	withholdsRouteTables bool
	ignoresGate          bool
}

func (r *Stacks) IgnoreGate() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ignoresGate = true
}

func (r *Stacks) functionsIgnoreGate(spec provider.StackSpec) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ignoresGate && spec.Edge != nil && spec.Edge.Facts().GatesAtOrigin
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

func (r *Stacks) Destroying(hook func(provider.StackRef) error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.destroying = hook
}

func (r *Stacks) PlanningDestroy(teardown func(provider.StackRef) provider.Plan) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.teardown = teardown
}

func (r *Stacks) Provisioned() []provider.StackSpec {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.provisioned)
}

func (r *Stacks) Plan(ctx context.Context, spec provider.StackSpec, _ progress.Log) (provider.Plan, error) {
	spec, imaged, err := r.withFunctionImages(ctx, spec)
	if err != nil {
		return provider.Plan{}, err
	}
	if imaged {
		return resources.SynthesizedPlan(ctx, spec, r.Inspect(spec.Ref).Result)
	}
	staged, err := functioncode.Stage(spec, nil)
	if err != nil {
		return provider.Plan{}, err
	}
	defer functioncode.Discard(staged)
	shipped, err := functioncode.Rows(ctx, r.artifacts, staged)
	if err != nil {
		return provider.Plan{}, err
	}
	return resources.SynthesizedPlan(ctx, spec, r.Inspect(spec.Ref).Result, shipped...)
}

func (r *Stacks) PlanDestroy(_ context.Context, ref provider.StackRef, _ progress.Log) (provider.Plan, error) {
	r.mu.Lock()
	teardown := r.teardown
	r.mu.Unlock()
	if teardown != nil {
		return teardown(ref), nil
	}
	return resources.SynthesizedRemoval(ref, r.Inspect(ref).Result), nil
}

func (r *Stacks) Provision(ctx context.Context, spec provider.StackSpec, progress progress.Log) (provider.StackResult, error) {
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
	if !spec.Recorded && r.Inspect(spec.Ref).Present {
		return provider.StackResult{}, refusal.Refuse(refusal.CodeNotReady,
			"%s already exists and this project has no record of it: ocel deploys over what it provisioned itself, never over what it finds. "+
				"Remove it, or deploy this project under another name", spec.Ref.Name)
	}
	spec, imaged, err := r.withFunctionImages(ctx, spec)
	if err != nil {
		return provider.StackResult{}, err
	}
	if err := spec.Images.PushMissing(ctx, progress); err != nil {
		return provider.StackResult{}, err
	}
	var staged []functioncode.Upload
	if !imaged {
		if staged, err = functioncode.Stage(spec, nil); err != nil {
			return provider.StackResult{}, err
		}
	}
	defer functioncode.Discard(staged)
	if err := functioncode.Ship(ctx, r.artifacts, staged, progress); err != nil {
		return provider.StackResult{}, err
	}
	r.recordShipped(spec, staged)
	result := provider.StackResult{}
	for _, resource := range spec.Resources {
		if resource.Binding != "" {
			continue
		}
		result.Bindings = append(result.Bindings, r.bindingFor(resource))
	}
	result.Functions = ProvisionedFunctions(spec)
	if r.functionsIgnoreGate(spec) {
		for at := range result.Functions {
			result.Functions[at].IgnoresGate = true
		}
	}
	result.Containers = ProvisionedContainers(spec)
	if spec.App != nil {
		result.EdgeBundleKey = r.deliveredEdgeBundle(spec)
		result.RouteTableKey = r.deliverRouteTable(spec)
		if spec.App.ISR != nil {
			result.PurgeSecret = "isr-" + spec.Ref.Name.String()
		}
	}
	if err := spec.Images.PushRemoved(ctx, progress); err != nil {
		return provider.StackResult{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.provisioned = append(r.provisioned, spec)
	r.stacks[stackKey(spec.Ref)] = result
	if progress != nil {
		progress.Say("Provisioned stack " + spec.Ref.Name.String())
	}
	return result, nil
}

func (r *Stacks) RefuseNextDestroy(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refusal = err
}

func (r *Stacks) DestroyedWith() []provider.ImageStore {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.destroyedWith)
}

func (r *Stacks) ListDestroysUnderLease() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.leasedDestroys)
}

func (r *Stacks) Destroy(_ context.Context, req provider.StackDestroy, progress progress.Log) error {
	ref, images := req.Ref, req.Images
	r.journal.note("destroy " + ref.Name.String())
	r.mu.Lock()
	destroying := r.destroying
	if req.Lease.Covers(ref) {
		r.leasedDestroys = append(r.leasedDestroys, ref.Name.String())
	}
	r.mu.Unlock()
	if destroying != nil {
		if err := destroying(ref); err != nil {
			return err
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.destroyedWith = append(r.destroyedWith, images)
	if r.refusal != nil {
		refused := r.refusal
		r.refusal = nil
		return refused
	}
	delete(r.stacks, stackKey(ref))
	if progress != nil {
		progress.Say("Destroyed stack " + ref.Name.String())
	}
	return nil
}

type InspectedStack struct {
	Present bool
	Result  provider.StackResult
}

func (r *Stacks) Inspect(ref provider.StackRef) InspectedStack {
	r.mu.Lock()
	defer r.mu.Unlock()
	result, present := r.stacks[stackKey(ref)]
	return InspectedStack{Present: present, Result: result}
}

func (r *Stacks) recordShipped(spec provider.StackSpec, staged []functioncode.Upload) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.shipped == nil {
		r.shipped = map[string][]functioncode.Upload{}
	}
	r.shipped[stackKey(spec.Ref)] = staged
}

func (r *Stacks) Shipped(ref provider.StackRef) []functioncode.Upload {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.shipped[stackKey(ref)])
}

func (r *Stacks) WithholdRouteTables() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.withholdsRouteTables = true
}

func (r *Stacks) RouteTable(key string) ([]byte, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	table, delivered := r.routeTables[key]
	return slices.Clone(table), delivered
}

func (r *Stacks) deliverRouteTable(spec provider.StackSpec) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if spec.App.EdgeRouteTable == nil || r.withholdsRouteTables {
		return ""
	}
	if r.routeTables == nil {
		r.routeTables = map[string][]byte{}
	}
	r.routeTables[spec.App.EdgeRouteTable.Location.Key] = slices.Clone(spec.App.EdgeRouteTable.Table)
	return spec.App.EdgeRouteTable.Location.Key
}

func (r *Stacks) deliveredEdgeBundle(spec provider.StackSpec) string {
	root := buildoutput.AppRoot(spec.App.OutputRoot, spec.App.App)
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(edge.AppBundleFile))); err != nil {
		return ""
	}
	return spec.Ref.Name.String() + "/edge/bundle.json"
}

func ProvisionedFunctions(spec provider.StackSpec) []provider.Function {
	if spec.App == nil {
		return nil
	}
	functions := make([]provider.Function, 0, len(spec.App.Functions))
	for _, function := range spec.App.Functions {
		physical := spec.Ref.Name.String() + "-" + function.Name
		functions = append(functions, provider.Function{
			Name:          function.Name,
			Physical:      physical,
			URL:           "https://" + physical + ".fn.fake.invalid",
			DeploymentURL: deploymentURL(spec, physical),
		})
	}
	if len(functions) == 0 {
		return nil
	}
	return functions
}

const DeploymentURLMarker = ".deployment."

func deploymentURL(spec provider.StackSpec, physical string) string {
	if spec.Ref.Tier != environment.TierPreview {
		return ""
	}
	return "https://" + naming.Sanitize(physical) + DeploymentURLMarker + "fake.invalid"
}

const DiscoveredHealthPath = "/up"

func ProvisionedContainers(spec provider.StackSpec) []provider.AppContainer {
	if spec.App == nil || spec.App.Compute != provider.ComputeContainer {
		return nil
	}
	physical := spec.Ref.Name.String() + "-" + spec.App.App
	return []provider.AppContainer{{
		Name:                      spec.App.App,
		Physical:                  physical,
		URL:                       "https://" + physical + ".ctr.fake.invalid",
		DeploymentURL:             deploymentURL(spec, physical),
		Image:                     spec.App.Image,
		DiscoveredHealthCheckPath: discoveredHealthPath(spec.App),
	}}
}

func discoveredHealthPath(app *provider.AppSpec) string {
	switch {
	case app.HealthCheckPath != "":
		return ""
	case app.DiscoveredHealthCheckPath != "":
		return app.DiscoveredHealthCheckPath
	default:
		return DiscoveredHealthPath
	}
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
	return ref.Project + "/" + string(ref.Tier) + "/" + ref.Name.String()
}

func (r *Stacks) bindingFor(resource provider.Resource) provider.Binding {
	binding := provider.Binding{
		Type:       resource.Type,
		Name:       resource.Name,
		Properties: propertiesFor(resource.Type, resource.Name),
		Grants:     r.Grants,
	}
	if resource.Type != provider.BindingPostgres {
		return binding
	}
	if r.PasswordSecret == "" {
		binding.Properties[provider.PropertyPassword] = "fake-password"
		return binding
	}
	binding.Grants = append(slices.Clone(r.Grants), provider.Grant{
		Label:     provider.GrantPasswordSecret,
		Actions:   []string{"fake:ReadSecret"},
		Resources: []string{r.PasswordSecret},
	})
	return binding
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

func (*Provider) ProvisionFunctions(_ context.Context, spec provider.StackSpec, _ progress.Log) ([]provider.Function, error) {
	return ProvisionedFunctions(spec), nil
}

func (p *Provider) RemoveFunctions(_ context.Context, _ provider.StackRef, functions []provider.Function, _ provider.ImageStore, _ progress.Log) error {
	for _, function := range functions {
		p.stacks.recordDestroyed(function.Name)
	}
	return nil
}

func (*Provider) ProvisionContainers(_ context.Context, spec provider.StackSpec, _ progress.Log) ([]provider.AppContainer, error) {
	return ProvisionedContainers(spec), nil
}

func (p *Provider) RemoveContainers(_ context.Context, _ provider.StackRef, containers []provider.AppContainer, _ provider.ImageStore, _ progress.Log) error {
	for _, container := range containers {
		p.stacks.recordDestroyed(container.Name)
	}
	return nil
}
