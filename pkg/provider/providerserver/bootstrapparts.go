package providerserver

import (
	"context"
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/pricing"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

const partScope = "part"

type estimate func(ctx context.Context, req *costv1.PriceRequest) (*costv1.Estimate, error)

func bootstrapPartsOf(ctx context.Context, r bootstrapplan.Readiness, status bootstrapplan.Status, price estimate) ([]*contractv1.BootstrapPart, error) {
	described, err := r.Bootstrap.DescribeParts(ctx, status.Description())
	if err != nil {
		return nil, err
	}
	records, err := r.PartRecords(ctx, status.Tier)
	if err != nil {
		return nil, err
	}
	recorded, err := r.RecordedParts(ctx, status.Tier)
	if err != nil {
		return nil, err
	}
	idle, currency, err := idleCosts(ctx, described, price)
	if err != nil {
		return nil, err
	}
	offer := r.Bootstrap.Parts()
	wants, err := r.WantedParts(ctx, status.Tier, offer, nil)
	if err != nil {
		return nil, err
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
		if cost, known := idle[part.Name]; known {
			out.IdleMonthlyFixed, out.Currency = &cost, currency
		}
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
	return parts, nil
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

func idleCosts(ctx context.Context, parts []provider.BootstrapPart, price estimate) (map[string]string, string, error) {
	tree := &pricing.Tree{}
	scopes := map[string]string{}
	for _, part := range parts {
		if part.Shape == nil {
			continue
		}
		scope := tree.Scope("", partScope, part.Name)
		scopes[scope] = part.Name
		tree.AddShaped(scope, part.Shape.Vendor, part.Shape.Region, part.Shape.Resources)
	}
	if len(scopes) == 0 || price == nil {
		return nil, "", nil
	}
	set, err := tree.Set(provider.CostSource)
	if err != nil {
		return nil, "", err
	}
	estimated, err := price(ctx, &costv1.PriceRequest{Resources: set})
	if err != nil {
		return nil, "", err
	}
	unpriced := map[string]bool{}
	scopeOf := map[string]string{}
	for _, resource := range set.GetResources() {
		scopeOf[resource.GetId()] = resource.GetScope()
	}
	for _, resource := range estimated.GetResources() {
		switch resource.GetStatus() {
		case costv1.ResourceEstimate_STATUS_UNSUPPORTED, costv1.ResourceEstimate_STATUS_NO_PRICE:
			unpriced[scopeOf[resource.GetResource()]] = true
		}
	}
	idle := map[string]string{}
	for _, total := range estimated.GetScopes() {
		if name, ok := scopes[total.GetScope()]; ok && !unpriced[total.GetScope()] {
			idle[name] = total.GetMonthlyFixed()
		}
	}
	return idle, estimated.GetCurrency(), nil
}
