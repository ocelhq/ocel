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

func (b *Bootstrap) Stands(kinds ...edge.Kind) {
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
