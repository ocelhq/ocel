package gcp

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
)

const (
	reasonPartFirstUse = "added on first use: %s"
	reasonPartBehind   = "made by another build, and this one writes it differently"
	reasonPartClaimed  = "another run is adding it"
)

type part struct {
	name        string
	summary     string
	content     string
	apis        []string
	retires     bool
	resources   []provider.PartResource
	shape       []pricing.Shaped
	make        func(ctx context.Context) error
	users       func(ctx context.Context) ([]string, error)
	refuseInUse func(ctx context.Context, removal partRemoval) error
	remove      func(ctx context.Context) error
}

func (p part) digest() string {
	sum := sha256.New()
	sum.Write([]byte(p.name + "\n" + p.content + "\n"))
	for _, resource := range p.resources {
		sum.Write([]byte(resource.Kind + "/" + resource.ID + "\n"))
	}
	return hex.EncodeToString(sum.Sum(nil))
}

func (b bootstrap) listDeployedParts(tier environment.Tier) []part {
	var parts []part
	for _, each := range removableParts {
		if each.deployed != nil {
			parts = append(parts, each.deployed(b, tier))
		}
	}
	return parts
}

func (b bootstrap) ensurePart(ctx context.Context, tier environment.Tier, project string, want part) error {
	return stackrecords.EnsureBootstrapPart(ctx, b.records, tier, b.partWork(tier, project, want))
}

func (b bootstrap) partWork(tier environment.Tier, project string, want part) stackrecords.PartWork {
	return stackrecords.PartWork{
		Name:      want.name,
		WrittenBy: string(b.writtenBy),
		Digest:    want.digest(),
		AddedBy:   project,
		Make: func(ctx context.Context) ([]provider.PartResource, error) {
			if err := b.servicesOn(ctx, want.apis); err != nil {
				return nil, err
			}
			if err := want.make(ctx); err != nil {
				return nil, fmt.Errorf("add the %s part of the %s bootstrap: %w", want.name, tier, err)
			}
			return want.resources, nil
		},
	}
}

func (b bootstrap) planPart(ctx context.Context, tier environment.Tier, want part) (provider.ChangeGroup, bool, error) {
	recorded, present, err := stackrecords.ReadBootstrapPart(ctx, b.records, tier, want.name)
	if err != nil {
		return provider.ChangeGroup{}, false, err
	}
	if present && recorded.CurrentFor(string(b.writtenBy), want.digest()) {
		return provider.ChangeGroup{}, false, nil
	}
	group := provider.ChangeGroup{
		Kind:   provider.StackGroupKind,
		Name:   b.partGroupName(tier, want.name),
		Part:   want.name,
		Action: provider.ActionCreate,
		Reason: fmt.Sprintf(reasonPartFirstUse, want.summary),
	}
	change := provider.ActionCreate
	switch {
	case present && recorded.State != stackrecords.PartReady:
		group.Reason = reasonPartClaimed
	case present:
		group.Action, group.Reason, change = provider.ActionUpdate, reasonPartBehind, provider.ActionUpdate
	}
	for _, resource := range want.resources {
		group.Changes = append(group.Changes, provider.Change{Kind: resource.Kind, Name: resource.ID, Action: change})
	}
	return group, true, nil
}

func (b bootstrap) describePart(ctx context.Context, tier environment.Tier, want part) (provider.BootstrapPart, bool, error) {
	recorded, present, err := stackrecords.ReadBootstrapPart(ctx, b.records, tier, want.name)
	if err != nil || !present {
		return provider.BootstrapPart{}, false, err
	}
	described := provider.BootstrapPart{
		Name:                want.name,
		Summary:             want.summary,
		Current:             recorded.CurrentFor(string(b.writtenBy), want.digest()),
		Digest:              recorded.Digest,
		Resources:           want.resources,
		RetiresWithLastUser: want.retires,
		Shape:               &provider.PartShape{Vendor: string(Vendor), Region: b.clients.Region, Resources: want.shape},
	}
	if want.users != nil {
		if described.UsedBy, err = want.users(ctx); err != nil {
			return provider.BootstrapPart{}, false, err
		}
	}
	return described, true, nil
}

func (b bootstrap) listRecordedParts(ctx context.Context, tier environment.Tier) ([]part, error) {
	var recorded []part
	for _, each := range b.listDeployedParts(tier) {
		_, present, err := stackrecords.ReadBootstrapPart(ctx, b.records, tier, each.name)
		if err != nil {
			return nil, err
		}
		if present {
			recorded = append(recorded, each)
		}
	}
	return recorded, nil
}

func (b bootstrap) listDescribedParts(ctx context.Context, tier environment.Tier) ([]provider.BootstrapPart, error) {
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

func (b bootstrap) refusePartsInUse(ctx context.Context, tier environment.Tier) error {
	recorded, err := b.listRecordedParts(ctx, tier)
	if err != nil {
		return err
	}
	for _, each := range recorded {
		if each.refuseInUse == nil {
			continue
		}
		if err := each.refuseInUse(ctx, partRemoval{retry: "ocel bootstrap destroy " + string(tier)}); err != nil {
			return err
		}
	}
	return nil
}

func (b bootstrap) listPartRemovals(ctx context.Context, tier environment.Tier) ([]provider.ChangeGroup, error) {
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

func (b bootstrap) partGroupName(tier environment.Tier, name string) string {
	return string(Vendor) + "/" + b.clients.Project() + "/" + string(tier) + "/" + name
}

func (b bootstrap) removalGroup(tier environment.Tier, each part) provider.ChangeGroup {
	group := provider.ChangeGroup{
		Kind:   provider.StackGroupKind,
		Name:   b.partGroupName(tier, each.name),
		Part:   each.name,
		Action: provider.ActionDelete,
	}
	for _, resource := range each.resources {
		group.Changes = append(group.Changes, provider.Change{Kind: resource.Kind, Name: resource.ID, Action: provider.ActionDelete})
	}
	return group
}

func (b bootstrap) removeParts(ctx context.Context, tier environment.Tier, progress progress.Log) error {
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
