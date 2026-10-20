package providerserver

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func (h *handlers) ListProjects(ctx context.Context, _ *contractv1.ListProjectsRequest) (*contractv1.ListProjectsResponse, error) {
	vendor, err := h.session.use()
	if err != nil {
		return nil, err
	}
	listed, err := listProjectSummaries(ctx, vendor.KeyValues())
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	return &contractv1.ListProjectsResponse{Projects: listed}, nil
}

func listProjectSummaries(ctx context.Context, store records.Store) ([]*contractv1.ProjectSummary, error) {
	recorded, err := listRecordedProjects(ctx, store)
	if err != nil {
		return nil, err
	}
	summaries := make([]*contractv1.ProjectSummary, 0, len(recorded))
	for _, project := range recorded {
		active, err := readActiveProduction(ctx, store, project)
		if err != nil {
			return nil, err
		}
		previews, err := readPreviews(ctx, store, project)
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, project.summary(active.PromotionID, len(previews)))
	}
	return summaries, nil
}

func (h *handlers) GetProject(ctx context.Context, req *contractv1.GetProjectRequest) (*contractv1.GetProjectResponse, error) {
	vendor, err := h.session.use()
	if err != nil {
		return nil, err
	}
	got, err := getProject(ctx, vendor.KeyValues(), req.GetSlug())
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	return got, nil
}

func getProject(ctx context.Context, store records.Store, slug string) (*contractv1.GetProjectResponse, error) {
	if slug == "" {
		return nil, errUnnamedProject
	}
	project, found, err := readRecordedProject(ctx, store, slug)
	if err != nil {
		return nil, err
	}
	if !found {
		return &contractv1.GetProjectResponse{}, nil
	}
	selection, err := edgeSelectionOf(project.edge())
	if err != nil {
		return nil, err
	}
	got := &contractv1.GetProjectResponse{Edge: selection, Registry: imageRegistryOf(project.registry())}
	for _, tier := range project.tiers() {
		got.Tiers = append(got.Tiers, encodeTier(tier))
	}
	return got, nil
}

func (h *handlers) DescribeProject(ctx context.Context, req *contractv1.DescribeProjectRequest) (*contractv1.DescribeProjectResponse, error) {
	vendor, err := h.session.use()
	if err != nil {
		return nil, err
	}
	described, err := h.describeProject(ctx, vendor, req)
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	return described, nil
}

func (h *handlers) describeProject(ctx context.Context, vendor provider.Provider, req *contractv1.DescribeProjectRequest) (*contractv1.DescribeProjectResponse, error) {
	if req.GetSlug() == "" {
		return nil, errUnnamedProject
	}
	store := vendor.KeyValues()
	project, found, err := readRecordedProject(ctx, store, req.GetSlug())
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, refusal.Refuse(refusal.CodeInvalid, "no project named %s is recorded in this account", req.GetSlug())
	}
	described := &contractv1.DescribeProjectResponse{}

	active, err := readActiveProduction(ctx, store, project)
	if err != nil {
		return nil, err
	}
	previews, err := readPreviews(ctx, store, project)
	if err != nil {
		return nil, err
	}
	described.Project = project.summary(active.PromotionID, len(previews))

	for _, tier := range project.tiers() {
		stacks, err := stackrecords.List(ctx, store, tier, project.slug)
		if err != nil {
			return nil, err
		}
		served := edgeStateStore{keyValues: store, name: stackrecords.EdgeStackKey(tier, project.slug)}
		state, err := served.read(ctx)
		if err != nil {
			return nil, err
		}
		if tier == environment.TierProduction && len(stacks) > 0 {
			described.Environments = append(described.Environments, describeProduction(h.session.edgeFacts(), state, active))
		}
		if tier == environment.TierPreview {
			environments, err := describePreviews(ctx, h.session.edgeFacts(), store, project.slug, previews)
			if err != nil {
				return nil, err
			}
			described.Environments = append(described.Environments, environments...)
		}
		groups, err := listStackResources(ctx, vendor, tier, project.slug, stacks)
		if err != nil {
			return nil, err
		}
		described.Resources = append(described.Resources, groups...)
		outside, err := listResourcesOutsideStacks(ctx, vendor, tier, project, state, stacks)
		if err != nil {
			return nil, err
		}
		if outside != nil {
			described.Resources = append(described.Resources, outside)
		}
	}

	if described.Shared, err = h.listSharedParts(ctx, project); err != nil {
		return nil, err
	}
	return described, nil
}
