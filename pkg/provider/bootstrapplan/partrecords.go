package bootstrapplan

import (
	"context"
	"errors"
	"maps"
	"slices"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func (s Status) Description() provider.BootstrapDescription {
	return provider.BootstrapDescription{
		Tier:       s.Tier,
		Present:    s.Present,
		Stacks:     s.Stacks,
		Unfinished: s.Unfinished,
	}
}

func (r Readiness) PartRecords(ctx context.Context, tier environment.Tier) (map[string]stackrecords.BootstrapPart, error) {
	records, err := stackrecords.ListBootstrapParts(ctx, r.KeyValues, tier)
	if storeAbsent(err) {
		return map[string]stackrecords.BootstrapPart{}, nil
	}
	return records, err
}

func UsersOf(part provider.BootstrapPart, recorded map[string][]string, offered bool) []string {
	switch {
	case part.UsedBy != nil:
		return part.UsedBy
	case part.Name == provider.BasePart || part.UsedByEveryProject:
		return slices.Sorted(maps.Keys(recorded))
	case offered:
		return ProjectsDependingOn(recorded, []string{part.Name})
	}
	return nil
}

func (r Readiness) claimParts(ctx context.Context, status Status, adding []string, wants map[string][]string) (map[string]stackrecords.BootstrapPart, error) {
	var names []string
	if !status.Present {
		names = append(names, provider.BasePart)
	}
	for _, part := range adding {
		if !slices.Contains(status.Parts, part) {
			names = append(names, part)
		}
	}
	claimed := map[string]stackrecords.BootstrapPart{}
	for _, name := range names {
		claim := stackrecords.BootstrapPart{WrittenBy: string(r.WrittenBy), AddedWith: addedWithBootstrap, AddedAt: time.Now().UTC()}
		if wanting := wants[name]; len(wanting) > 0 {
			claim.AddedBy = wanting[0]
		}
		claim, won, err := stackrecords.ClaimBootstrapPart(ctx, r.KeyValues, status.Tier, name, claim)
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

const addedWithBootstrap = "ocel bootstrap"

func (r Readiness) recordParts(ctx context.Context, tier environment.Tier, parts, removed []string) error {
	status, err := r.Status(ctx, tier)
	if err != nil {
		return err
	}
	described, err := r.Bootstrap.DescribeParts(ctx, status.Description())
	if err != nil {
		return err
	}
	named := make(map[string]provider.BootstrapPart, len(described))
	for _, part := range described {
		named[part.Name] = part
	}
	for _, name := range append([]string{provider.BasePart}, parts...) {
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

func (r Readiness) forgetWants(ctx context.Context, tier environment.Tier, wants map[string][]string) error {
	for _, part := range slices.Sorted(maps.Keys(wants)) {
		if err := stackrecords.ForgetBootstrapWants(ctx, r.KeyValues, tier, part); err != nil {
			return err
		}
	}
	return nil
}

func (r Readiness) forgetParts(ctx context.Context, tier environment.Tier) error {
	records, err := r.PartRecords(ctx, tier)
	if err != nil {
		return err
	}
	for name := range records {
		if err := stackrecords.ForgetBootstrapPart(ctx, r.KeyValues, tier, name); err != nil {
			return err
		}
	}
	wants, err := stackrecords.ListBootstrapWants(ctx, r.KeyValues, tier)
	if storeAbsent(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return r.forgetWants(ctx, tier, wants)
}
