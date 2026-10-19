package providerserver

import (
	"context"
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/pkg/environment"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func listStackResources(ctx context.Context, vendor provider.Provider, tier environment.Tier, slug string, stacks []stackrecords.NamedStack) ([]*contractv1.ResourceGroup, error) {
	newest := stackrecords.NewestReleases(stacks)
	var groups []*contractv1.ResourceGroup
	for _, stack := range stacks {
		listed, err := vendor.Stacks().Resources(ctx, provider.StackResources{
			Ref:           provider.StackRef{Project: slug, Tier: tier, Name: stack.Name},
			NewestRelease: slices.Contains(newest, stack.Name),
		})
		if err != nil {
			return nil, fmt.Errorf("list what %s holds: %w", stack.Name, err)
		}
		if len(listed) == 0 {
			continue
		}
		groups = append(groups, &contractv1.ResourceGroup{
			Tier:        encodeTier(tier),
			Environment: stack.Name.Env,
			Stack:       stack.Name.String(),
			Resources:   encodePhysicalResources(listed),
		})
	}
	return groups, nil
}

func listResourcesOutsideStacks(ctx context.Context, vendor provider.Provider, tier environment.Tier, project recordedProject, state stackrecords.EdgeState, stacks []stackrecords.NamedStack) (*contractv1.ResourceGroup, error) {
	list := vendor.Hooks().ListProjectResources
	if list == nil {
		return nil, nil
	}
	req := provider.ProjectResourcesRequest{
		Tier:           tier,
		Project:        project.slug,
		EdgeKind:       state.Kind,
		EdgeStack:      state.Edge,
		Routers:        state.Routers,
		WrittenRecords: state.WrittenRecords(),
		Certificates:   state.Certificates(),
		DNSKind:        provider.DNSKind(project.records[tier].Edge.DNSKind),
		Images:         listStackImages(stacks),
	}
	listed, err := list(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("list what %s holds for %s outside its stacks in %s: %w", vendor.Facts().Vendor, project.slug, tier, err)
	}
	if len(listed) == 0 {
		return nil, nil
	}
	return &contractv1.ResourceGroup{Tier: encodeTier(tier), Resources: encodePhysicalResources(listed)}, nil
}

func listStackImages(stacks []stackrecords.NamedStack) []string {
	var images []string
	for _, stack := range stacks {
		images = append(images, stack.Images()...)
	}
	slices.Sort(images)
	return slices.Compact(images)
}

func encodePhysicalResources(listed []provider.PhysicalResource) []*contractv1.PhysicalResource {
	encoded := make([]*contractv1.PhysicalResource, 0, len(listed))
	for _, resource := range listed {
		encoded = append(encoded, &contractv1.PhysicalResource{
			Kind:       resource.Kind,
			Id:         resource.ID,
			Location:   resource.Location,
			ConsoleUrl: resource.ConsoleURL,
		})
	}
	return encoded
}
