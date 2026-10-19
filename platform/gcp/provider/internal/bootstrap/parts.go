package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

const (
	reasonPartFirstUse = "added on first use: %s"
	reasonPartBehind   = "made by another build, and this one writes it differently"
	reasonPartClaimed  = "another run is adding it"
)

type Part struct {
	Name        string
	Summary     string
	Content     string
	APIs        []string
	Retires     bool
	Resources   []provider.PhysicalResource
	Shape       []pricing.Shaped
	Make        func(ctx context.Context) error
	Claim       func(ctx context.Context, project string) error
	Users       func(ctx context.Context) ([]string, error)
	RefuseInUse func(ctx context.Context, removal PartRemoval) error
	Remove      func(ctx context.Context) error
}

func (p Part) digest() string {
	sum := sha256.New()
	sum.Write([]byte(p.Name + "\n" + p.Content + "\n"))
	for _, resource := range p.Resources {
		sum.Write([]byte(resource.Kind + "/" + resource.ID + "\n"))
	}
	return hex.EncodeToString(sum.Sum(nil))
}

func (b Engine) listDeployedParts(tier environment.Tier) []Part {
	var parts []Part
	for _, each := range b.Removable {
		if each.Deployed != nil {
			parts = append(parts, each.Deployed(b, tier))
		}
	}
	return parts
}

func (b Engine) EnsurePart(ctx context.Context, tier environment.Tier, project string, want Part) error {
	return stackrecords.EnsureBootstrapPart(ctx, b.Records, tier, b.partWork(tier, project, want))
}

func (b Engine) partWork(tier environment.Tier, project string, want Part) stackrecords.PartWork {
	return stackrecords.PartWork{
		Name:      want.Name,
		WrittenBy: string(b.WrittenBy),
		Digest:    want.digest(),
		AddedBy:   project,
		Make: func(ctx context.Context) ([]provider.PhysicalResource, error) {
			if err := b.servicesOn(ctx, want.APIs); err != nil {
				return nil, err
			}
			if err := want.Make(ctx); err != nil {
				return nil, fmt.Errorf("add the %s part of the %s bootstrap: %w", want.Name, tier, err)
			}
			return want.Resources, nil
		},
	}
}

func (b Engine) planPart(ctx context.Context, tier environment.Tier, want Part) (provider.ChangeGroup, bool, error) {
	recorded, present, err := stackrecords.ReadBootstrapPart(ctx, b.Records, tier, want.Name)
	if err != nil {
		return provider.ChangeGroup{}, false, err
	}
	if present && recorded.CurrentFor(string(b.WrittenBy), want.digest()) {
		return provider.ChangeGroup{}, false, nil
	}
	group := provider.ChangeGroup{
		Kind:   provider.StackGroupKind,
		Name:   b.partGroupName(tier, want.Name),
		Part:   want.Name,
		Action: provider.ActionCreate,
		Reason: fmt.Sprintf(reasonPartFirstUse, want.Summary),
	}
	change := provider.ActionCreate
	switch {
	case present && recorded.State != stackrecords.PartReady:
		group.Reason = reasonPartClaimed
	case present:
		group.Action, group.Reason, change = provider.ActionUpdate, reasonPartBehind, provider.ActionUpdate
	}
	for _, resource := range want.Resources {
		group.Changes = append(group.Changes, provider.Change{Kind: resource.Kind, Name: resource.ID, Action: change})
	}
	return group, true, nil
}

func (b Engine) describePart(ctx context.Context, tier environment.Tier, want Part) (provider.BootstrapPart, bool, error) {
	recorded, present, err := stackrecords.ReadBootstrapPart(ctx, b.Records, tier, want.Name)
	if err != nil || !present {
		return provider.BootstrapPart{}, false, err
	}
	described := provider.BootstrapPart{
		Name:                want.Name,
		Summary:             want.Summary,
		Current:             recorded.CurrentFor(string(b.WrittenBy), want.digest()),
		Digest:              recorded.Digest,
		Resources:           want.Resources,
		RetiresWithLastUser: want.Retires,
		Shape:               &provider.PartShape{Vendor: string(gcloud.Vendor), Region: b.Clients.Region, Resources: want.Shape},
	}
	if want.Users != nil {
		if described.UsedBy, err = want.Users(ctx); err != nil {
			return provider.BootstrapPart{}, false, err
		}
	}
	return described, true, nil
}

func (b Engine) listRecordedParts(ctx context.Context, tier environment.Tier) ([]Part, error) {
	var recorded []Part
	for _, each := range b.listDeployedParts(tier) {
		_, present, err := stackrecords.ReadBootstrapPart(ctx, b.Records, tier, each.Name)
		if err != nil {
			return nil, err
		}
		if present {
			recorded = append(recorded, each)
		}
	}
	return recorded, nil
}

func (b Engine) ListDescribedParts(ctx context.Context, tier environment.Tier) ([]provider.BootstrapPart, error) {
	var described []provider.BootstrapPart
	for _, each := range b.listDeployedParts(tier) {
		found, present, err := b.describePart(ctx, tier, each)
		if err != nil {
			return nil, err
		}
		if present {
			described = append(described, found)
		}
	}
	return described, nil
}

func (b Engine) RefusePartsInUse(ctx context.Context, tier environment.Tier) error {
	recorded, err := b.listRecordedParts(ctx, tier)
	if err != nil {
		return err
	}
	for _, each := range recorded {
		if each.RefuseInUse == nil {
			continue
		}
		if err := each.RefuseInUse(ctx, PartRemoval{Retry: "ocel bootstrap destroy " + string(tier)}); err != nil {
			return err
		}
	}
	return nil
}

func (b Engine) ListPartRemovals(ctx context.Context, tier environment.Tier) ([]provider.ChangeGroup, error) {
	recorded, err := b.listRecordedParts(ctx, tier)
	if err != nil {
		return nil, err
	}
	groups := make([]provider.ChangeGroup, 0, len(recorded))
	for _, each := range recorded {
		groups = append(groups, b.removalGroup(tier, each))
	}
	return groups, nil
}

func (b Engine) partGroupName(tier environment.Tier, name string) string {
	return string(gcloud.Vendor) + "/" + b.Clients.Project() + "/" + string(tier) + "/" + name
}

func (b Engine) removalGroup(tier environment.Tier, each Part) provider.ChangeGroup {
	group := provider.ChangeGroup{
		Kind:   provider.StackGroupKind,
		Name:   b.partGroupName(tier, each.Name),
		Part:   each.Name,
		Action: provider.ActionDelete,
	}
	for _, resource := range each.Resources {
		group.Changes = append(group.Changes, provider.Change{Kind: resource.Kind, Name: resource.ID, Action: provider.ActionDelete})
	}
	return group
}

func (b Engine) RemoveParts(ctx context.Context, tier environment.Tier, progress progress.Log) error {
	recorded, err := b.listRecordedParts(ctx, tier)
	if err != nil {
		return err
	}
	for _, each := range slices.Backward(recorded) {
		if err := b.removePart(ctx, tier, each, progress); err != nil {
			return err
		}
	}
	return nil
}

func (b Engine) RepairParts(ctx context.Context, tier environment.Tier) ([]string, error) {
	var works []stackrecords.PartWork
	for _, each := range b.Removable {
		if each.Work != nil {
			works = append(works, each.Work(b, tier))
		}
	}
	for _, each := range b.listDeployedParts(tier) {
		works = append(works, b.partWork(tier, "", each))
	}
	return stackrecords.RepairBootstrapParts(ctx, b.Records, tier, works)
}

func (b Engine) AddParts(ctx context.Context, pre provider.DeployPreflight, names []string) error {
	tier := pre.Deploy.Tier
	for _, want := range b.listDeployedParts(tier) {
		if !slices.Contains(names, want.Name) {
			continue
		}
		if want.Claim != nil {
			if err := want.Claim(ctx, pre.Deploy.Slug); err != nil {
				return err
			}
		}
		if err := b.EnsurePart(ctx, tier, pre.Deploy.Slug, want); err != nil {
			return err
		}
	}
	return nil
}

func (b Engine) PlanParts(ctx context.Context, tier environment.Tier, names []string) ([]provider.ChangeGroup, error) {
	var groups []provider.ChangeGroup
	for _, want := range b.listDeployedParts(tier) {
		if !slices.Contains(names, want.Name) {
			continue
		}
		group, planned, err := b.planPart(ctx, tier, want)
		if err != nil {
			return nil, err
		}
		if planned {
			groups = append(groups, group)
		}
	}
	return groups, nil
}
