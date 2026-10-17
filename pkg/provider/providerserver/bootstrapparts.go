package providerserver

import (
	"context"
	"errors"
	"maps"
	"slices"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

const partScope = "part"

type estimate func(ctx context.Context, req *costv1.PriceRequest) (*costv1.Estimate, error)

func (s BootstrapStatus) description() provider.BootstrapDescription {
	return provider.BootstrapDescription{
		Tier:        s.Tier,
		Present:     s.Present,
		Stacks:      s.Stacks,
		Unfinished:  s.Unfinished,
		VendorState: s.VendorState,
	}
}

func (r Readiness) Parts(ctx context.Context, status BootstrapStatus, price estimate) ([]*contractv1.BootstrapPart, error) {
	described, err := r.Bootstrap.DescribeParts(ctx, status.description())
	if err != nil {
		return nil, err
	}
	records, err := r.recordedParts(ctx, status.Tier)
	if err != nil {
		return nil, err
	}
	recorded, err := r.RecordedFeatures(ctx, status.Tier)
	if err != nil {
		return nil, err
	}
	idle, currency, err := idleCosts(ctx, described, price)
	if err != nil {
		return nil, err
	}
	catalogued := map[string]bool{}
	for _, feature := range r.Bootstrap.Catalogue() {
		catalogued[feature.Name] = true
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
			UsedBy:  usersOf(part, recorded, catalogued[part.Name]),
		}
		for _, resource := range part.Resources {
			out.Resources = append(out.Resources, &contractv1.BootstrapPartResource{Kind: resource.Kind, Id: resource.ID})
		}
		if cost, known := idle[part.Name]; known {
			out.IdleMonthlyFixed, out.Currency = &cost, currency
		}
		if record, ok := records[part.Name]; ok {
			applyRecord(out, record)
			delete(records, part.Name)
		}
		parts = append(parts, out)
	}
	for _, name := range slices.Sorted(maps.Keys(records)) {
		out := &contractv1.BootstrapPart{Name: name}
		applyRecord(out, records[name])
		for _, resource := range records[name].Resources {
			out.Resources = append(out.Resources, &contractv1.BootstrapPartResource{Kind: resource.Kind, Id: resource.ID})
		}
		parts = append(parts, out)
	}
	return parts, nil
}

func boolOrder(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (r Readiness) recordedParts(ctx context.Context, tier environment.Tier) (map[string]stackrecords.BootstrapPart, error) {
	records, err := stackrecords.ListBootstrapParts(ctx, r.KeyValues, tier)
	if storeAbsent(err) {
		return map[string]stackrecords.BootstrapPart{}, nil
	}
	return records, err
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

func usersOf(part provider.BootstrapPart, recorded map[string][]string, catalogued bool) []string {
	switch {
	case part.UsedBy != nil:
		return part.UsedBy
	case part.Name == provider.BasePart || part.UsedByEveryProject:
		return slices.Sorted(maps.Keys(recorded))
	case catalogued:
		return ProjectsDependingOn(recorded, []string{part.Name})
	}
	return nil
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

func (r Readiness) claimParts(ctx context.Context, status BootstrapStatus, adding []string) (map[string]stackrecords.BootstrapPart, error) {
	var names []string
	if !status.Present {
		names = append(names, provider.BasePart)
	}
	for _, feature := range adding {
		if !slices.Contains(status.Features, feature) {
			names = append(names, feature)
		}
	}
	claimed := map[string]stackrecords.BootstrapPart{}
	for _, name := range names {
		claim, won, err := stackrecords.ClaimBootstrapPart(ctx, r.KeyValues, status.Tier, name, stackrecords.BootstrapPart{
			WrittenBy: string(r.WrittenBy),
			AddedAt:   time.Now().UTC(),
		})
		if storeAbsent(err) {
			return claimed, nil
		}
		if err != nil {
			return claimed, err
		}
		if won {
			claimed[name] = claim
		}
	}
	return claimed, nil
}

func (r Readiness) releaseParts(ctx context.Context, tier environment.Tier, claimed map[string]stackrecords.BootstrapPart) error {
	settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), claimReleaseTimeout)
	defer cancel()
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(claimed)) {
		errs = append(errs, stackrecords.ReleaseBootstrapPart(settle, r.KeyValues, tier, name, claimed[name]))
	}
	return errors.Join(errs...)
}

const claimReleaseTimeout = 30 * time.Second

func (r Readiness) recordParts(ctx context.Context, tier environment.Tier, features, removed []string) error {
	status, err := r.Status(ctx, tier)
	if err != nil {
		return err
	}
	described, err := r.Bootstrap.DescribeParts(ctx, status.description())
	if err != nil {
		return err
	}
	named := make(map[string]provider.BootstrapPart, len(described))
	for _, part := range described {
		named[part.Name] = part
	}
	for _, name := range append([]string{provider.BasePart}, features...) {
		if err := stackrecords.MarkBootstrapPartReady(ctx, r.KeyValues, tier, name, stackrecords.BootstrapPart{
			WrittenBy: string(r.WrittenBy),
			Digest:    named[name].Digest,
			AddedAt:   time.Now().UTC(),
			Resources: named[name].Resources,
		}); err != nil {
			return err
		}
	}
	for _, name := range removed {
		if err := stackrecords.ForgetBootstrapPart(ctx, r.KeyValues, tier, name); err != nil {
			return err
		}
	}
	return nil
}

func (r Readiness) forgetParts(ctx context.Context, tier environment.Tier) error {
	records, err := r.recordedParts(ctx, tier)
	if err != nil {
		return err
	}
	for name := range records {
		if err := stackrecords.ForgetBootstrapPart(ctx, r.KeyValues, tier, name); err != nil {
			return err
		}
	}
	return nil
}
