package providerserver

import (
	"context"
	"slices"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func (h *handlers) listSharedParts(ctx context.Context, project recordedProject) ([]*contractv1.SharedPart, error) {
	var shared []*contractv1.SharedPart
	for _, tier := range project.tiers() {
		_, readiness, err := h.readiness(string(project.records[tier].Edge.Kind))
		if err != nil {
			return nil, err
		}
		status, err := readiness.Status(ctx, tier)
		if isUnbootstrapped(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !status.Present {
			continue
		}
		parts, _, err := bootstrapPartsOf(ctx, readiness, status)
		if err != nil {
			return nil, err
		}
		for _, part := range parts {
			if slices.Contains(part.GetUsedBy(), project.slug) {
				shared = append(shared, &contractv1.SharedPart{Tier: encodeTier(tier), Part: part})
			}
		}
	}
	return shared, nil
}
