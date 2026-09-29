package run

import (
	"fmt"
	"sort"

	"github.com/ocelhq/ocel/pkg/edge"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	rankStack = iota
	rankEdge
	rankOther
)

var groupRanks = map[string]int{
	provider.StackGroupKind:     rankStack,
	provider.ParameterGroupKind: rankStack,
	edge.EdgeGroupKind:          rankEdge,
}

func IsCoreGroupKind(kind string) bool {
	_, core := groupRanks[kind]
	return core
}

func ActingChanges(changes []*planv1.Change) []*planv1.Change {
	acting := make([]*planv1.Change, 0, len(changes))
	for _, change := range changes {
		if change.GetAction() != planv1.Change_ACTION_KEEP {
			acting = append(acting, change)
		}
	}
	return acting
}

func groupRank(kind string) int {
	if rank, named := groupRanks[kind]; named {
		return rank
	}
	return rankOther
}

func orderPlan(plan *planv1.ChangePlan) {
	if plan == nil {
		return
	}
	sort.SliceStable(plan.Groups, func(i, j int) bool {
		return groupRank(plan.Groups[i].GetKind()) < groupRank(plan.Groups[j].GetKind())
	})
	for _, group := range plan.GetGroups() {
		sort.SliceStable(group.Changes, func(i, j int) bool {
			return changeKey(group.Changes[i]) < changeKey(group.Changes[j])
		})
	}
}

func changeKey(c *planv1.Change) string {
	return fmt.Sprintf("%s\x00%s\x00%d", c.GetKind(), c.GetName(), c.GetAction())
}
