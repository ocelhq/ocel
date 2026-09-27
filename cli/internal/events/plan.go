package events

import (
	"fmt"
	"sort"

	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	rankSpineHead = iota
	rankSpineEdge
	rankOffSpine
)

var spineRanks = map[string]int{
	provider.StackGroupKind:     rankSpineHead,
	provider.ParameterGroupKind: rankSpineHead,
	edge.EdgeGroupKind:          rankSpineEdge,
}

func spineRank(kind string) int {
	if rank, named := spineRanks[kind]; named {
		return rank
	}
	return rankOffSpine
}

func orderPlan(plan *planv1.ChangePlan) {
	if plan == nil {
		return
	}
	sort.SliceStable(plan.Groups, func(i, j int) bool {
		return spineRank(plan.Groups[i].GetKind()) < spineRank(plan.Groups[j].GetKind())
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
