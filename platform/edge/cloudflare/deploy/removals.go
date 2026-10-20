package cloudflare

import "github.com/ocelhq/ocel/pkg/edge"

func (p *cloudflare) PlanProjectRemoval(scope edge.ProjectScope) ([]edge.PlanGroup, error) {
	changes := []edge.PlanChange{{
		Kind:   kindWorker,
		Name:   scope.Slug,
		Action: edge.PlanDelete,
		Reason: "the entry worker this project deployed, and the routes that reach it",
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
		Reason: "every deployment and pointer this project promoted",
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
			{Kind: kindWorker, Name: previewEntryScript, Action: edge.PlanDelete,
				Reason: "the shared entry worker this wildcard is owned by"},
		},
	}
	kept := edge.PlanGroup{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(Kind),
		Action: edge.PlanKeep,
		Reason: "bootstrap-scoped: `ocel bootstrap destroy preview` removes what bootstrap provisioned",
	}
	return removed, kept, nil
}

func (p *cloudflare) PlanSharedPreviewRemoval() edge.PlanGroup {
	return edge.PlanGroup{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(Kind),
		Action: edge.PlanKeep,
		Reason: "bootstrap-scoped: " + edge.PreviewEntryOwner + " fronts every project's previews",
	}
}
