package fake

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	FeatureCache  = "cache"
	FeatureImages = "images"
)

type Bootstrap struct {
	mu       sync.Mutex
	applied  map[providerkit.Class][]string
	behind   map[string]bool
	writer   string
	schema   uint32
	refusal  error
	requests []providerkit.BootstrapRequest
	front    edge.Kind
	standing []edge.Kind
	halfway  bool
}

func NewBootstrap() *Bootstrap {
	return &Bootstrap{
		applied: map[providerkit.Class][]string{},
		behind:  map[string]bool{},
		writer:  "1.0.0",
		schema:  providerkit.BootstrapSchema,
	}
}

func (b *Bootstrap) fronting(kind edge.Kind) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.front = kind
}

func (b *Bootstrap) Standing(kinds ...edge.Kind) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.standing = append(make([]edge.Kind, 0, len(kinds)), kinds...)
}

func (b *Bootstrap) Fronting() edge.Kind {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.front
}

func (b *Bootstrap) Catalogue() []providerkit.Feature {
	return []providerkit.Feature{
		{
			Name:    FeatureCache,
			Summary: "the reference provider's response cache",
			Needs:   []string{providerkit.NeedsFrameworkPrefix + "next"},
		},
		{
			Name:      FeatureImages,
			Summary:   "the reference provider's image optimizer",
			DependsOn: []string{FeatureCache},
			Needs:     []string{providerkit.NeedsFrameworkPrefix + "next", providerkit.NeedsEdgePrefix + "relay"},
		},
	}
}

func (b *Bootstrap) Behind(features ...string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, feature := range features {
		b.behind[feature] = true
	}
}

func (b *Bootstrap) WrittenBy(writer string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.writer = writer
}

func (b *Bootstrap) AtSchema(schema uint32) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.schema = schema
}

func (b *Bootstrap) Halfway() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.halfway = true
}

func (b *Bootstrap) RefuseApply(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refusal = err
}

func (b *Bootstrap) Applied() []providerkit.BootstrapRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.requests)
}

func (b *Bootstrap) Describe(_ context.Context, class providerkit.Class) (providerkit.BootstrapReading, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	features, present := b.applied[class]
	described := providerkit.BootstrapReading{Class: class, Present: present, Unfinished: present && b.halfway}
	if !present {
		return described, nil
	}
	described.Stacks = append(described.Stacks, b.stack(class, ""))
	for _, feature := range features {
		described.Stacks = append(described.Stacks, b.stack(class, feature))
	}
	return described, nil
}

func stackNameOf(class providerkit.Class, feature string) string {
	name := "fake-" + string(class)
	if feature != "" {
		name += "-" + feature
	}
	return name
}

func (b *Bootstrap) stack(class providerkit.Class, feature string) providerkit.BootstrapStack {
	return providerkit.BootstrapStack{
		Name:          stackNameOf(class, feature),
		Feature:       feature,
		Present:       true,
		Schema:        b.schema,
		DigestCurrent: !b.behind[feature],
		WrittenBy:     b.writer,
	}
}

func (b *Bootstrap) Plan(ctx context.Context, req providerkit.BootstrapRequest) (providerkit.Plan, error) {
	described, err := b.Describe(ctx, req.Class)
	if err != nil {
		return providerkit.Plan{}, err
	}
	groups := providerkit.DeriveGroups(b.named(described), b.Catalogue(), req)
	for i, group := range groups {
		if group.Action == providerkit.ActionKeep {
			continue
		}
		groups[i].Changes = []providerkit.Change{{
			Kind:   "Fake::Stack::Resource",
			Name:   group.Name + "-resource",
			Action: group.Action,
		}}
	}
	return providerkit.Plan{Groups: groups}, nil
}

func (b *Bootstrap) named(described providerkit.BootstrapReading) providerkit.BootstrapReading {
	return providerkit.NameStacks(described, b.Catalogue(), func(feature string) string {
		return stackNameOf(described.Class, feature)
	})
}

func (b *Bootstrap) Apply(_ context.Context, req providerkit.BootstrapRequest, progress providerkit.Progress) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.refusal != nil {
		return b.refusal
	}
	b.requests = append(b.requests, req)
	b.applied[req.Class] = slices.Clone(req.Features)
	b.behind = map[string]bool{}
	if progress != nil {
		progress.Say("bootstrapped " + string(req.Class))
	}
	return nil
}

func (b *Bootstrap) PlanRemove(_ context.Context, class providerkit.Class) (providerkit.Plan, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	features, present := b.applied[class]
	if !present {
		return providerkit.Plan{}, nil
	}
	plan := providerkit.Plan{Groups: make([]providerkit.ChangeGroup, 0, len(features)+1)}
	for _, feature := range features {
		plan.Groups = append(plan.Groups, providerkit.ChangeGroup{
			Kind:    providerkit.StackGroupKind,
			Name:    b.stack(class, feature).Name,
			Feature: feature,
			Action:  providerkit.ActionDelete,
		})
	}
	plan.Groups = append(plan.Groups, providerkit.ChangeGroup{
		Kind:   providerkit.StackGroupKind,
		Name:   b.stack(class, "").Name,
		Action: providerkit.ActionDelete,
		Reason: "the core every feature above was built on",
		Slow:   true,
	})
	for _, kind := range b.standingEdges() {
		plan.Groups = append(plan.Groups, providerkit.ChangeGroup{
			Kind:   providerkit.EdgeGroupKind,
			Name:   edge.EdgeGroupName(kind),
			Action: providerkit.ActionDelete,
			Changes: []providerkit.Change{{
				Kind:   "Fake::Edge::Front",
				Name:   string(kind) + "-front",
				Action: providerkit.ActionDelete,
			}},
		})
	}
	return plan, nil
}

func (b *Bootstrap) standingEdges() []edge.Kind {
	if b.standing != nil {
		return b.standing
	}
	if b.front == "" {
		return nil
	}
	return []edge.Kind{b.front}
}

func (b *Bootstrap) Remove(_ context.Context, class providerkit.Class, progress providerkit.Progress) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.applied, class)
	if progress != nil {
		progress.Say("removed " + string(class))
	}
	return nil
}

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

func (r *Stacks) Plan(ctx context.Context, plan providerkit.StackPlan, _ providerkit.Progress) (providerkit.Plan, error) {
	return providerkit.SynthesizedPlan(ctx, r.artifacts, plan, r.State(plan.Ref).Result)
}

func (r *Stacks) PlanDestroy(_ context.Context, ref providerkit.StackRef, _ providerkit.Progress) (providerkit.Plan, error) {
	return providerkit.SynthesizedRemoval(ref, r.State(ref).Result), nil
}

func (r *Stacks) Provision(ctx context.Context, plan providerkit.StackPlan, progress providerkit.Progress) (providerkit.StackResult, error) {
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

func (r *Stacks) Destroy(_ context.Context, ref providerkit.StackRef, progress providerkit.Progress) error {
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
	root := providerkit.AppArtifactRoot(providerkit.ArtifactRoot(), plan.App.App)
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

type Credentials struct {
	mu      sync.Mutex
	region  string
	refusal error
}

func NewCredentials(region string) *Credentials { return &Credentials{region: region} }

func (c *Credentials) Deny(hint string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refusal = providerkit.Refuse(providerkit.CodeDenied, "%s", hint)
}

func (c *Credentials) Admit() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refusal = nil
}

func (c *Credentials) Whoami(context.Context) (providerkit.Identity, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.refusal != nil {
		return providerkit.Identity{}, c.refusal
	}
	return providerkit.Identity{
		Provider:  Vendor,
		Account:   "000000000000",
		Principal: "fake/reference",
		Details:   []providerkit.Detail{{Label: "region", Value: c.region}},
	}, nil
}

func (c *Credentials) Permissions(tier providerkit.CredentialTier) (edge.CredentialDocument, error) {
	return edge.CredentialDocument{
		Heading:  "fake credentials",
		Document: "fake permissions for " + string(tier),
	}, nil
}
