package cost

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/structpb"

	"github.com/ocelhq/ocel/cli/internal/pricecache"
	"github.com/ocelhq/ocel/pkg/pricing"
	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
)

type byProfile map[costv1.Profile]*costv1.Estimate

func estimateSets(ctx context.Context, catalog *pricecache.Catalog, table pricing.Table, overrides map[string]*structpb.Struct, offline bool, sets ...*costv1.ResourceSet) ([]byProfile, error) {
	usages, err := usagePerSet(overrides, sets)
	if err != nil {
		return nil, err
	}
	profiles := pricing.Profiles()
	inputs := make([]pricing.Input, 0, len(sets)*len(profiles))
	for i, set := range sets {
		for _, profile := range profiles {
			inputs = append(inputs, pricing.Input{Set: set, Usage: &costv1.Usage{Profile: profile, Resources: usages[i]}})
		}
	}
	estimates, err := pricing.EstimateAll(ctx, catalog, table, inputs)
	if err != nil {
		return nil, err
	}
	all := make([]byProfile, 0, len(sets))
	for i := range sets {
		each := make(byProfile, len(profiles))
		for j, profile := range profiles {
			each[profile] = estimates[i*len(profiles)+j]
		}
		all = append(all, each)
	}
	if oldest := catalog.OldestCached(); offline && !oldest.IsZero() {
		note := "prices cached up to " + ago(time.Since(oldest)) + " ago"
		for _, each := range all {
			for _, estimate := range each {
				estimate.Notes = append(estimate.Notes, note)
			}
		}
	}
	return all, nil
}

func usagePerSet(overrides map[string]*structpb.Struct, sets []*costv1.ResourceSet) ([]map[string]*structpb.Struct, error) {
	if len(sets) == 1 {
		return []map[string]*structpb.Struct{overrides}, nil
	}
	named := make(map[string]bool, len(overrides))
	usages := make([]map[string]*structpb.Struct, 0, len(sets))
	for _, set := range sets {
		usage := map[string]*structpb.Struct{}
		for id := range resourceIDs(set) {
			if quantities, ok := overrides[id]; ok {
				usage[id] = quantities
				named[id] = true
			}
		}
		usages = append(usages, usage)
	}
	for _, id := range slices.Sorted(maps.Keys(overrides)) {
		if !named[id] {
			return nil, &pricing.UsageError{Resource: id, Reason: "which is in neither set this scan compares"}
		}
	}
	return usages, nil
}

func resourceIDs(set *costv1.ResourceSet) map[string]bool {
	ids := make(map[string]bool, len(set.GetResources()))
	for _, resource := range set.GetResources() {
		ids[resource.GetId()] = true
	}
	return ids
}

func ago(d time.Duration) string {
	switch {
	case d < 2*time.Minute:
		return "a minute"
	case d < 2*time.Hour:
		return fmt.Sprintf("%d minutes", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d hours", int(d/time.Hour))
	}
	return fmt.Sprintf("%d days", int(d/(24*time.Hour)))
}

func catalogLabel(versions []*costv1.CatalogVersion) string {
	labels := make([]string, 0, len(versions))
	for _, version := range versions {
		labels = append(labels, version.GetVendor()+" "+version.GetService()+" "+version.GetVersion())
	}
	return strings.Join(labels, ", ")
}
