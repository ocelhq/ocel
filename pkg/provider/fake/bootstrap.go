package fake

import (
	"context"
	"slices"
	"sync"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
)

const (
	PartCache  = "cache"
	PartImages = "images"
)

type Bootstrap struct {
	mu          sync.Mutex
	applied     map[environment.Tier][]string
	stale       map[string]bool
	unreadable  map[string]string
	writer      string
	refusal     error
	requests    []provider.BootstrapRequest
	front       edge.Kind
	raised      []edge.Kind
	unfinished  bool
	offered     []provider.Part
	planned     *provider.Plan
	planRefusal error
	removal     *provider.Plan
	described   int
	parts       map[environment.Tier][]provider.BootstrapPart
}

func NewBootstrap() *Bootstrap {
	return &Bootstrap{
		applied:    map[environment.Tier][]string{},
		stale:      map[string]bool{},
		unreadable: map[string]string{},
		writer:     "1.0.0",
		parts:      map[environment.Tier][]provider.BootstrapPart{},
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

func (b *Bootstrap) Offers(offered ...provider.Part) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.offered = offered
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

func (b *Bootstrap) Parts() []provider.Part {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.offeredLocked()
}

func (b *Bootstrap) offeredLocked() []provider.Part {
	if b.offered != nil {
		return slices.Clone(b.offered)
	}
	return []provider.Part{
		{
			Name:    PartCache,
			Summary: "the reference provider's response cache",
		},
		{
			Name:    PartImages,
			Summary: "the reference provider's image optimizer",
			Edges:   []edge.Kind{"relay"},
		},
	}
}

func (b *Bootstrap) MarkStale(parts ...string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, part := range parts {
		b.stale[part] = true
	}
}

func (b *Bootstrap) MarkUnreadable(part, cause string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.unreadable[part] = cause
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

func (b *Bootstrap) Described() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.described
}

func (b *Bootstrap) Describe(_ context.Context, tier environment.Tier) (provider.BootstrapDescription, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.described++
	parts, present := b.applied[tier]
	described := provider.BootstrapDescription{Tier: tier, Present: present, Unfinished: present && b.unfinished}
	if !present {
		return described, nil
	}
	described.Stacks = append(described.Stacks, b.stack(tier, ""))
	for _, part := range parts {
		described.Stacks = append(described.Stacks, b.stack(tier, part))
	}
	return described, nil
}

func stackNameOf(tier environment.Tier, part string) string {
	name := "fake-" + string(tier)
	if part != "" {
		name += "-" + part
	}
	return name
}

func (b *Bootstrap) stack(tier environment.Tier, part string) provider.BootstrapStack {
	return provider.BootstrapStack{
		Name:          stackNameOf(tier, part),
		Part:          part,
		Present:       true,
		DigestCurrent: !b.stale[part] && b.unreadable[part] == "",
		WrittenBy:     b.writer,
		ReadError:     b.unreadable[part],
	}
}

func (b *Bootstrap) AddPart(tier environment.Tier, part provider.BootstrapPart) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.parts[tier] = append(b.parts[tier], part)
}

func (b *Bootstrap) DescribeParts(_ context.Context, described provider.BootstrapDescription) ([]provider.BootstrapPart, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var parts []provider.BootstrapPart
	for _, stack := range described.Stacks {
		name := stack.Part
		if name == "" {
			name = provider.BasePart
		}
		parts = append(parts, provider.BootstrapPart{
			Name:      name,
			Current:   stack.DigestCurrent,
			Digest:    "digest-" + stack.Name,
			Resources: []provider.PartResource{{Kind: "Fake::Stack", ID: stack.Name}},
			Shape: &provider.PartShape{
				Vendor:    string(Vendor),
				Resources: []pricing.Shaped{{Name: stack.Name, Type: TypeBucket, Properties: map[string]any{}}},
			},
		})
	}
	return append(parts, b.parts[described.Tier]...), nil
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
	groups := bootstrapplan.ChangeGroups(b.withDefaultStackNames(described), b.Parts(), req)
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
	return bootstrapplan.WithDefaultStackNames(described, b.Parts(), func(part string) string {
		return stackNameOf(described.Tier, part)
	})
}

func (b *Bootstrap) Apply(_ context.Context, req provider.BootstrapRequest, progress progress.Log) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.refusal != nil {
		return b.refusal
	}
	b.requests = append(b.requests, req)
	installed := slices.Clone(b.applied[req.Tier])
	for _, part := range req.Parts {
		if !slices.Contains(installed, part) {
			installed = append(installed, part)
		}
	}
	installed = slices.DeleteFunc(installed, func(part string) bool { return slices.Contains(req.Remove, part) })
	b.applied[req.Tier] = bootstrapplan.InOfferedOrder(b.offeredLocked(), installed)
	if b.applied[req.Tier] == nil {
		b.applied[req.Tier] = []string{}
	}
	b.parts[req.Tier] = slices.DeleteFunc(b.parts[req.Tier], func(part provider.BootstrapPart) bool { return slices.Contains(req.Remove, part.Name) })
	b.stale = map[string]bool{}
	b.unreadable = map[string]string{}
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
	parts, present := b.applied[tier]
	if !present {
		return provider.Plan{}, nil
	}
	plan := provider.Plan{Groups: make([]provider.ChangeGroup, 0, len(parts)+1)}
	for _, part := range parts {
		plan.Groups = append(plan.Groups, provider.ChangeGroup{
			Kind:   provider.StackGroupKind,
			Name:   b.stack(tier, part).Name,
			Part:   part,
			Action: provider.ActionDelete,
		})
	}
	plan.Groups = append(plan.Groups, provider.ChangeGroup{
		Kind:   provider.StackGroupKind,
		Name:   b.stack(tier, "").Name,
		Action: provider.ActionDelete,
		Reason: "the core every part above was built on",
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
	delete(b.parts, tier)
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
