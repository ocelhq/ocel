package providerserver

import (
	"context"
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/pricing"
	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func bootstrapPartsOf(ctx context.Context, r bootstrapplan.Readiness, status bootstrapplan.Status) ([]*contractv1.BootstrapPart, *costv1.ResourceSet, error) {
	described, err := r.Bootstrap.DescribeParts(ctx, status.Description())
	if err != nil {
		return nil, nil, err
	}
	records, err := r.PartRecords(ctx, status.Tier)
	if err != nil {
		return nil, nil, err
	}
	recorded, err := r.RecordedParts(ctx, status.Tier)
	if err != nil {
		return nil, nil, err
	}
	inventory, err := partInventory(described)
	if err != nil {
		return nil, nil, err
	}
	offer := r.Bootstrap.Parts()
	wants, err := r.WantedParts(ctx, status.Tier, offer, nil)
	if err != nil {
		return nil, nil, err
	}
	offered := map[string]bool{}
	summaries := map[string]string{}
	for _, part := range offer {
		offered[part.Name] = true
		summaries[part.Name] = part.Summary
	}

	slices.SortStableFunc(described, func(a, b provider.BootstrapPart) int {
		return boolOrder(b.Name == provider.BasePart) - boolOrder(a.Name == provider.BasePart)
	})
	parts := make([]*contractv1.BootstrapPart, 0, len(described)+len(records))
	for _, part := range described {
		out := &contractv1.BootstrapPart{
			Name:    part.Name,
			Summary: part.Summary,
			State:   contractv1.BootstrapPartState_BOOTSTRAP_PART_STATE_READY,
			Current: part.Current,
			UsedBy:  bootstrapplan.UsersOf(part, recorded, offered[part.Name]),

			RetiresWithLastUser: part.RetiresWithLastUser,
		}
		out.Resources = encodePhysicalResources(part.Resources)
		if record, ok := records[part.Name]; ok {
			applyRecord(out, record)
			delete(records, part.Name)
		}
		delete(wants, part.Name)
		parts = append(parts, out)
	}
	for _, name := range slices.Sorted(maps.Keys(records)) {
		out := &contractv1.BootstrapPart{Name: name}
		applyRecord(out, records[name])
		out.Resources = encodePhysicalResources(records[name].Resources)
		delete(wants, name)
		parts = append(parts, out)
	}
	for _, name := range slices.Sorted(maps.Keys(wants)) {
		parts = append(parts, &contractv1.BootstrapPart{
			Name:     name,
			Summary:  summaries[name],
			State:    contractv1.BootstrapPartState_BOOTSTRAP_PART_STATE_WANTED,
			WantedBy: slices.Sorted(slices.Values(wants[name])),
		})
	}
	return parts, inventory, nil
}

func boolOrder(b bool) int {
	if b {
		return 1
	}
	return 0
}

func applyRecord(out *contractv1.BootstrapPart, record stackrecords.BootstrapPart) {
	out.AddedBy, out.AddedWith = record.AddedBy, record.AddedWith
	if !record.AddedAt.IsZero() {
		out.AddedAt = record.AddedAt.Unix()
	}
	if record.State == stackrecords.PartCreating {
		out.State = contractv1.BootstrapPartState_BOOTSTRAP_PART_STATE_CREATING
	} else {
		out.State = contractv1.BootstrapPartState_BOOTSTRAP_PART_STATE_READY
	}
}

func partInventory(parts []provider.BootstrapPart) (*costv1.ResourceSet, error) {
	tree := &pricing.Tree{}
	for _, part := range parts {
		if part.Inventory == nil {
			continue
		}
		tree.AddItems(tree.Scope("", pricing.ScopePart, part.Name), part.Inventory.Vendor, part.Inventory.Region, part.Inventory.Resources)
	}
	return tree.Set(provider.InventorySource)
}
