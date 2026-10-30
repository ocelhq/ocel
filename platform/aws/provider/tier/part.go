package tier

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
)

type Part struct {
	Name    string
	Summary string

	Describe  func(ctx context.Context, tier environment.Tier) (provider.BootstrapPart, bool, error)
	Inventory func(tier environment.Tier) []pricing.Resource
	Removal   *Removal
	Teardown  *Removal
	Repair    func(ctx context.Context, tier environment.Tier) (bool, error)
	Use       *Use
	Added     *Added
}

type Removal struct {
	Plan   func(ctx context.Context, tier environment.Tier) (*provider.ChangeGroup, error)
	Remove func(ctx context.Context, tier environment.Tier, progress progress.Log) error
}

type Use struct {
	IsUsedBy func(uses []buildoutput.Uses, front edge.Kind) bool
}

type Added struct {
	Plan   func(ctx context.Context, tier environment.Tier) (*provider.ChangeGroup, error)
	Ensure func(ctx context.Context, tier environment.Tier, addedBy, addedWith string, say func(string)) ([]provider.PhysicalResource, error)
}

func (p Part) IsOffered() bool { return p.Removal != nil }

func (p Part) Offer() provider.Part {
	return provider.Part{Name: p.Name, Summary: p.Summary}
}

func (p Part) IsAddedFor(uses []buildoutput.Uses, front edge.Kind) bool {
	return p.Added != nil && p.Use != nil && p.Use.IsUsedBy(uses, front)
}

type Parts []Part

func (parts Parts) Named(name string) (Part, bool) {
	at := slices.IndexFunc(parts, func(part Part) bool { return part.Name == name })
	if at < 0 {
		return Part{}, false
	}
	return parts[at], true
}

func (parts Parts) Offered() []provider.Part {
	var offered []provider.Part
	for _, part := range parts {
		if part.IsOffered() {
			offered = append(offered, part.Offer())
		}
	}
	return offered
}

func (parts Parts) AddedFor(uses []buildoutput.Uses, front edge.Kind) Parts {
	var added Parts
	for _, part := range parts {
		if part.IsAddedFor(uses, front) {
			added = append(added, part)
		}
	}
	return added
}

func (parts Parts) Names() []string {
	names := make([]string, 0, len(parts))
	for _, part := range parts {
		names = append(names, part.Name)
	}
	return names
}

func Deletion(name, reason string, resources []provider.PhysicalResource) *provider.ChangeGroup {
	group := provider.ChangeGroup{
		Kind:   provider.StackGroupKind,
		Name:   "bootstrap part " + name,
		Part:   name,
		Action: provider.ActionDelete,
		Reason: reason,
	}
	for _, resource := range resources {
		group.Changes = append(group.Changes, provider.Change{Kind: resource.Kind, Name: resource.ID, Action: provider.ActionDelete})
	}
	return &group
}
