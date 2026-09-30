package fake

import (
	"context"
	"slices"
	"sync"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
)

const (
	FeatureCache  = "cache"
	FeatureImages = "images"
)

type Bootstrap struct {
	mu          sync.Mutex
	applied     map[environment.Tier][]string
	stale       map[string]bool
	writer      string
	refusal     error
	requests    []provider.BootstrapRequest
	front       edge.Kind
	raised      []edge.Kind
	unfinished  bool
	catalogue   []provider.Feature
	planned     *provider.Plan
	planRefusal error
	removal     *provider.Plan
}

func NewBootstrap() *Bootstrap {
	return &Bootstrap{
		applied: map[environment.Tier][]string{},
		stale:   map[string]bool{},
		writer:  "1.0.0",
	}
}

func (b *Bootstrap) setDefaultEdge(kind edge.Kind) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.front = kind
}

func (b *Bootstrap) SetRaisedEdges(kinds ...edge.Kind) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.raised = append(make([]edge.Kind, 0, len(kinds)), kinds...)
}

func (b *Bootstrap) DefaultEdge() edge.Kind {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.front
}

func (b *Bootstrap) Offers(catalogue ...provider.Feature) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.catalogue = catalogue
}

func (b *Bootstrap) PlansWith(plan provider.Plan) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.planned = &plan
}

func (b *Bootstrap) RefusePlan(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.planRefusal = err
}

func (b *Bootstrap) PlansRemovalWith(plan provider.Plan) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.removal = &plan
}

func (b *Bootstrap) Catalogue() []provider.Feature {
	b.mu.Lock()
	offered := b.catalogue
	b.mu.Unlock()
	if offered != nil {
		return slices.Clone(offered)
	}
	return []provider.Feature{
		{
			Name:       FeatureCache,
			Summary:    "the reference provider's response cache",
			Frameworks: []string{"next"},
		},
		{
			Name:       FeatureImages,
			Summary:    "the reference provider's image optimizer",
			DependsOn:  []string{FeatureCache},
			Frameworks: []string{"next"},
			Edges:      []edge.Kind{"relay"},
		},
	}
}

func (b *Bootstrap) MarkStale(features ...string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, feature := range features {
		b.stale[feature] = true
	}
}

func (b *Bootstrap) SetWriter(writer string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.writer = writer
}

func (b *Bootstrap) MarkUnfinished() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.unfinished = true
}

func (b *Bootstrap) RefuseApply(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refusal = err
}

func (b *Bootstrap) Applied() []provider.BootstrapRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.requests)
}

func (b *Bootstrap) Describe(_ context.Context, tier environment.Tier) (provider.BootstrapDescription, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	features, present := b.applied[tier]
	described := provider.BootstrapDescription{Tier: tier, Present: present, Unfinished: present && b.unfinished}
	if !present {
		return described, nil
	}
	described.Stacks = append(described.Stacks, b.stack(tier, ""))
	for _, feature := range features {
		described.Stacks = append(described.Stacks, b.stack(tier, feature))
	}
	return described, nil
}

func stackNameOf(tier environment.Tier, feature string) string {
	name := "fake-" + string(tier)
	if feature != "" {
		name += "-" + feature
	}
	return name
}

func (b *Bootstrap) stack(tier environment.Tier, feature string) provider.BootstrapStack {
	return provider.BootstrapStack{
		Name:          stackNameOf(tier, feature),
		Feature:       feature,
		Present:       true,
		DigestCurrent: !b.stale[feature],
		WrittenBy:     b.writer,
	}
}

func (b *Bootstrap) Plan(ctx context.Context, req provider.BootstrapRequest) (provider.Plan, error) {
	b.mu.Lock()
	planned, refusal := b.planned, b.planRefusal
	b.mu.Unlock()
	if refusal != nil {
		return provider.Plan{}, refusal
	}
	if planned != nil {
		return clonePlan(*planned), nil
	}
	described, err := b.Describe(ctx, req.Tier)
	if err != nil {
		return provider.Plan{}, err
	}
	groups := bootstrapplan.ChangeGroups(b.withDefaultStackNames(described), b.Catalogue(), req)
	for i, group := range groups {
		if group.Action == provider.ActionKeep {
			continue
		}
		groups[i].Changes = []provider.Change{{
			Kind:   "Fake::Stack::Resource",
			Name:   group.Name + "-resource",
			Action: group.Action,
		}}
	}
	return provider.Plan{Groups: groups}, nil
}

func (b *Bootstrap) withDefaultStackNames(described provider.BootstrapDescription) provider.BootstrapDescription {
	return bootstrapplan.WithDefaultStackNames(described, b.Catalogue(), func(feature string) string {
		return stackNameOf(described.Tier, feature)
	})
}

func (b *Bootstrap) Apply(_ context.Context, req provider.BootstrapRequest, progress progress.Log) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.refusal != nil {
		return b.refusal
	}
	b.requests = append(b.requests, req)
	b.applied[req.Tier] = slices.Clone(req.Features)
	b.stale = map[string]bool{}
	if progress != nil {
		progress.Say("Applied the " + string(req.Tier) + " bootstrap")
	}
	return nil
}

func (b *Bootstrap) PlanRemove(_ context.Context, tier environment.Tier) (provider.Plan, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.removal != nil {
		return clonePlan(*b.removal), nil
	}
	features, present := b.applied[tier]
	if !present {
		return provider.Plan{}, nil
	}
	plan := provider.Plan{Groups: make([]provider.ChangeGroup, 0, len(features)+1)}
	for _, feature := range features {
		plan.Groups = append(plan.Groups, provider.ChangeGroup{
			Kind:    provider.StackGroupKind,
			Name:    b.stack(tier, feature).Name,
			Feature: feature,
			Action:  provider.ActionDelete,
		})
	}
	plan.Groups = append(plan.Groups, provider.ChangeGroup{
		Kind:   provider.StackGroupKind,
		Name:   b.stack(tier, "").Name,
		Action: provider.ActionDelete,
		Reason: "the core every feature above was built on",
		Slow:   true,
	})
	for _, kind := range b.raisedEdges() {
		plan.Groups = append(plan.Groups, provider.ChangeGroup{
			Kind:   edge.EdgeGroupKind,
			Name:   edge.EdgeGroupName(kind),
			Action: provider.ActionDelete,
			Changes: []provider.Change{{
				Kind:   "Fake::Edge::Front",
				Name:   string(kind) + "-front",
				Action: provider.ActionDelete,
			}},
		})
	}
	return plan, nil
}

func (b *Bootstrap) raisedEdges() []edge.Kind {
	if b.raised != nil {
		return b.raised
	}
	if b.front == "" {
		return nil
	}
	return []edge.Kind{b.front}
}

func (b *Bootstrap) Remove(_ context.Context, tier environment.Tier, progress progress.Log) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.applied, tier)
	if progress != nil {
		progress.Say("Removed the " + string(tier) + " bootstrap")
	}
	return nil
}

func clonePlan(plan provider.Plan) provider.Plan {
	groups := make([]provider.ChangeGroup, len(plan.Groups))
	for i, group := range plan.Groups {
		group.Changes = slices.Clone(group.Changes)
		groups[i] = group
	}
	plan.Groups = groups
	return plan
}
