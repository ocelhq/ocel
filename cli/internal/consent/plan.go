package consent

import (
	"github.com/ocelhq/ocel/cli/internal/run"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

func Mutates(plan *planv1.ChangePlan) bool { return len(written(plan)) > 0 }

func ConfirmVerb(plan *planv1.ChangePlan) string {
	if w := written(plan); len(w) == 1 && w[provider.ActionCreate] {
		return "Create these"
	}
	return "Apply these changes"
}

func written(plan *planv1.ChangePlan) map[provider.ChangeAction]bool {
	actions := map[provider.ChangeAction]bool{}
	for _, group := range plan.GetGroups() {
		acting := []planv1.Change_Action{group.GetAction()}
		if changes := run.ActingChanges(group.GetChanges()); len(changes) > 0 {
			acting = acting[:0]
			for _, change := range changes {
				acting = append(acting, change.GetAction())
			}
		}
		for _, drawn := range acting {
			if action, known := provider.ActionFromProto(drawn); known && action != "" && action.Writes() {
				actions[action] = true
			}
		}
	}
	return actions
}
