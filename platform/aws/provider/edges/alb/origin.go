package alb

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
)

const kindHostRule = "AWS::ElasticLoadBalancingV2::ListenerRule"

func planProjectRemoval(scope edge.ProjectScope) []edge.PlanGroup {
	if len(scope.Hostnames) == 0 {
		return nil
	}
	group := edge.PlanGroup{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.OriginGroupName,
		Action: edge.PlanDelete,
		Reason: "the rules the load balancer routes this project's hostnames by",
	}
	for _, hostname := range scope.Hostnames {
		group.Changes = append(group.Changes, edge.PlanChange{Kind: kindHostRule, Name: hostname, Action: edge.PlanDelete})
	}
	return []edge.PlanGroup{group}
}

func refusePreviewEntry(_ context.Context, claim router.Claim) (edge.Origin, error) {
	return edge.Origin{}, refusal.Refuse(refusal.CodeInvalid,
		"the load balancer answers each preview on a host of its own, never a wildcard entry such as %s", claim.Hostname)
}
