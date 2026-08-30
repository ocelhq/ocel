package fake

import (
	"context"
	"slices"
	"sync"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	FeatureCache  = "cache"
	FeatureImages = "images"
)

type Bootstrapper struct {
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

func NewBootstrapper() *Bootstrapper {
	return &Bootstrapper{
		applied: map[providerkit.Class][]string{},
		behind:  map[string]bool{},
		writer:  "1.0.0",
		schema:  providerkit.BootstrapSchema,
	}
}

func (b *Bootstrapper) fronting(kind edge.Kind) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.front = kind
}

func (b *Bootstrapper) Standing(kinds ...edge.Kind) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.standing = append(make([]edge.Kind, 0, len(kinds)), kinds...)
}

func (b *Bootstrapper) Fronting() edge.Kind {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.front
}

func (b *Bootstrapper) Catalogue() []providerkit.Feature {
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

func (b *Bootstrapper) Behind(features ...string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, feature := range features {
		b.behind[feature] = true
	}
}

func (b *Bootstrapper) WrittenBy(writer string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.writer = writer
}

func (b *Bootstrapper) AtSchema(schema uint32) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.schema = schema
}

func (b *Bootstrapper) Halfway() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.halfway = true
}

func (b *Bootstrapper) RefuseApply(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refusal = err
}

func (b *Bootstrapper) Applied() []providerkit.BootstrapRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.requests)
}

func (b *Bootstrapper) Describe(_ context.Context, class providerkit.Class) (providerkit.Bootstrap, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	features, present := b.applied[class]
	described := providerkit.Bootstrap{Class: class, Present: present, Unfinished: present && b.halfway}
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

func (b *Bootstrapper) stack(class providerkit.Class, feature string) providerkit.BootstrapStack {
	return providerkit.BootstrapStack{
		Name:          stackNameOf(class, feature),
		Feature:       feature,
		Present:       true,
		Schema:        b.schema,
		DigestCurrent: !b.behind[feature],
		Writer:        b.writer,
	}
}

func (b *Bootstrapper) Plan(ctx context.Context, req providerkit.BootstrapRequest) (providerkit.Plan, error) {
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

func (b *Bootstrapper) named(described providerkit.Bootstrap) providerkit.Bootstrap {
	return providerkit.NameStacks(described, b.Catalogue(), func(feature string) string {
		return stackNameOf(described.Class, feature)
	})
}

func (b *Bootstrapper) Apply(_ context.Context, req providerkit.BootstrapRequest, report providerkit.Reporter) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.refusal != nil {
		return b.refusal
	}
	b.requests = append(b.requests, req)
	b.applied[req.Class] = slices.Clone(req.Features)
	b.behind = map[string]bool{}
	if report != nil {
		report.Say("bootstrapped " + string(req.Class))
	}
	return nil
}

func (b *Bootstrapper) PlanRemoval(_ context.Context, class providerkit.Class) (providerkit.Plan, error) {
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

func (b *Bootstrapper) standingEdges() []edge.Kind {
	if b.standing != nil {
		return b.standing
	}
	if b.front == "" {
		return nil
	}
	return []edge.Kind{b.front}
}

func (b *Bootstrapper) Remove(_ context.Context, class providerkit.Class, report providerkit.Reporter) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.applied, class)
	if report != nil {
		report.Say("removed " + string(class))
	}
	return nil
}

type Releaser struct {
	Grants []providerkit.Grant

	artifacts providerkit.ArtifactStore

	mu      sync.Mutex
	stacks  map[string]providerkit.StackResult
	plans   []providerkit.StackPlan
	entered func(providerkit.StackPlan) error
}

func NewReleaser(artifacts providerkit.ArtifactStore) *Releaser {
	return &Releaser{artifacts: artifacts, stacks: map[string]providerkit.StackResult{}}
}

func (r *Releaser) Entering(hook func(providerkit.StackPlan) error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entered = hook
}

func (r *Releaser) Plans() []providerkit.StackPlan {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.plans)
}

func (r *Releaser) Plan(ctx context.Context, plan providerkit.StackPlan, _ providerkit.Reporter) (providerkit.Plan, error) {
	return providerkit.SynthesizedPlan(ctx, r.artifacts, plan, r.State(plan.Ref).Result)
}

func (r *Releaser) PlanDestroy(_ context.Context, ref providerkit.StackRef, _ providerkit.Reporter) (providerkit.Plan, error) {
	return providerkit.SynthesizedRemoval(ref, r.State(ref).Result), nil
}

func (r *Releaser) Provision(ctx context.Context, plan providerkit.StackPlan, report providerkit.Reporter) (providerkit.StackResult, error) {
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
	if err := providerkit.ShipUploads(ctx, r.artifacts, plan.Uploads, report); err != nil {
		return providerkit.StackResult{}, err
	}
	result := providerkit.StackResult{}
	for _, resource := range plan.Resources {
		if resource.Linked {
			continue
		}
		result.Links = append(result.Links, providerkit.Link{
			Type:       resource.Type,
			Name:       resource.Name,
			Properties: propertiesFor(resource.Type, resource.Name),
			Grants:     r.Grants,
		})
	}
	if plan.App != nil {
		for _, function := range plan.App.Functions {
			physical := plan.Ref.Name.String() + "-" + function.Name
			result.Functions = append(result.Functions, providerkit.Function{
				Name:     function.Name,
				Physical: physical,
				URL:      "https://" + physical + ".fn.fake.invalid",
			})
		}
		if plan.App.Compute == providerkit.ComputeContainer {
			physical := plan.Ref.Name.String() + "-" + plan.App.App
			result.Containers = append(result.Containers, providerkit.AppContainer{
				Name:     plan.App.App,
				Physical: physical,
				URL:      "https://" + physical + ".ctr.fake.invalid",
			})
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.plans = append(r.plans, plan)
	r.stacks[stackKey(plan.Ref)] = result
	if report != nil {
		report.Say("provisioned " + plan.Ref.Name.String())
	}
	return result, nil
}

func (r *Releaser) Destroy(_ context.Context, ref providerkit.StackRef, report providerkit.Reporter) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.stacks, stackKey(ref))
	if report != nil {
		report.Say("destroyed " + ref.Name.String())
	}
	return nil
}

func (r *Releaser) State(ref providerkit.StackRef) providerkit.StackState {
	r.mu.Lock()
	defer r.mu.Unlock()
	result, present := r.stacks[stackKey(ref)]
	return providerkit.StackState{Present: present, Result: result}
}

func stackKey(ref providerkit.StackRef) string {
	return ref.Project + "/" + string(ref.Class) + "/" + ref.Name.String()
}

func propertiesFor(t providerkit.LinkType, name string) map[string]string {
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
