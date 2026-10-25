package cloudflare

import "github.com/ocelhq/ocel/pkg/edge"

func (p *cloudflare) PlanProjectRemoval(scope edge.ProjectScope) ([]edge.PlanGroup, error) {
	changes := []edge.PlanChange{{
		Kind:   kindWorker,
		Name:   scope.Slug,
		Action: edge.PlanDelete,
	}}
	for _, hostname := range scope.Hostnames {
		changes = append(changes, edge.PlanChange{
			Kind:   kindWorkerRoute,
			Name:   hostname,
			Action: edge.PlanDelete,
		})
	}
	changes = append(changes, edge.PlanChange{
		Kind:   kindDurableObject,
		Name:   scope.Slug,
		Action: edge.PlanDelete,
	})
	return []edge.PlanGroup{{
		Kind:    edge.EdgeGroupKind,
		Name:    edge.EdgeGroupName(Kind),
		Action:  edge.PlanDelete,
		Changes: changes,
	}}, nil
}

func (p *cloudflare) PlanPreviewWildcardRemoval(wildcard string) (edge.PlanGroup, edge.PlanGroup, error) {
	removed := edge.PlanGroup{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(Kind),
		Action: edge.PlanDelete,
		Changes: []edge.PlanChange{
			{Kind: kindWorkerRoute, Name: wildcard, Action: edge.PlanDelete},
			{Kind: kindWorker, Name: sharedEntryStem, Action: edge.PlanDelete},
		},
	}
	kept := edge.PlanGroup{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(Kind),
		Action: edge.PlanKeep,
		Reason: "bootstrap-scoped: `ocel bootstrap destroy preview` removes it",
	}
	return removed, kept, nil
}

func (p *cloudflare) PlanSharedPreviewRemoval() edge.PlanGroup {
	return edge.PlanGroup{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(Kind),
		Action: edge.PlanKeep,
		Reason: "bootstrap-scoped",
	}
}
