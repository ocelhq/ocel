package changeplan

import (
	"fmt"
	"io"
	"os"
	"strings"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

const BypassEnv = "OCEL_DESTROY_BYPASS_CONFIRMATION"

func BypassRequest() string {
	return strings.TrimSpace(os.Getenv(BypassEnv))
}

func Print(out io.Writer, header string, plan *contractv1.ChangePlan, footer ...string) {
	if kind := plan.GetEdgeKind(); kind != "" {
		header += fmt.Sprintf(", fronted by the %s edge", kind)
	}
	fmt.Fprintf(out, "%s:\n", header)

	var kept []*contractv1.ChangeGroup
	for _, group := range plan.GetGroups() {
		if group.GetAction() == contractv1.Change_ACTION_KEEP {
			kept = append(kept, group)
			continue
		}
		writeGroup(out, group)
	}
	for _, line := range footer {
		fmt.Fprintln(out, line)
	}
	if len(kept) > 0 {
		fmt.Fprintln(out, "Left in place:")
		for _, group := range kept {
			writeGroup(out, group)
		}
	}
}

func Render(out io.Writer, header string, plan *contractv1.ChangePlan) {
	if kind := plan.GetEdgeKind(); kind != "" {
		header += fmt.Sprintf(" (%s edge)", kind)
	}
	fmt.Fprintf(out, "%s:\n\n", header)
	for _, group := range plan.GetGroups() {
		writeGroup(out, group)
	}
	if tally := Tally(plan); tally != "" {
		fmt.Fprintf(out, "\n%s\n", tally)
	}
}

func writeGroup(out io.Writer, group *contractv1.ChangeGroup) {
	fmt.Fprintln(out, GroupLine(group))
	if group.GetAction() == contractv1.Change_ACTION_KEEP {
		return
	}
	for _, change := range group.GetChanges() {
		fmt.Fprintf(out, "    %s\n", changeLine(change))
	}
}

func GroupLine(group *contractv1.ChangeGroup) string {
	return line(group.GetAction(), name(group.GetKind(), group.GetName()), group.GetReason(), group.GetSlow())
}

func changeLine(change *contractv1.Change) string {
	label := change.GetName()
	if kind := change.GetKind(); kind != "" {
		label += " (" + kind + ")"
	}
	return line(change.GetAction(), label, change.GetReason(), change.GetSlow())
}

func line(action contractv1.Change_Action, label, reason string, slow bool) string {
	if words := actionWords(action); words != "" {
		label = words + " " + label
	}
	out := sigil(action) + " " + label
	if reason != "" {
		out += " — " + reason
	}
	if slow {
		out += " (this one is slow)"
	}
	return out
}

func name(kind, given string) string {
	if kind == "" {
		return given
	}
	return kind + " " + given
}

func sigil(action contractv1.Change_Action) string {
	switch action {
	case contractv1.Change_ACTION_CREATE:
		return "+"
	case contractv1.Change_ACTION_UPDATE:
		return "~"
	case contractv1.Change_ACTION_REPLACE:
		return "±"
	case contractv1.Change_ACTION_DELETE, contractv1.Change_ACTION_DISABLE_THEN_DELETE:
		return "–"
	case contractv1.Change_ACTION_KEEP:
		return " "
	default:
		return "?"
	}
}

func actionWords(action contractv1.Change_Action) string {
	switch action {
	case contractv1.Change_ACTION_DISABLE_THEN_DELETE:
		return "disable, then delete"
	case contractv1.Change_ACTION_CREATE,
		contractv1.Change_ACTION_UPDATE,
		contractv1.Change_ACTION_REPLACE,
		contractv1.Change_ACTION_DELETE,
		contractv1.Change_ACTION_KEEP:
		return ""
	default:
		return fmt.Sprintf("act on (%s, an action this CLI does not know)", action)
	}
}

func AllKeep(plan *contractv1.ChangePlan) bool {
	groups := plan.GetGroups()
	if len(groups) == 0 {
		return false
	}
	for _, group := range groups {
		if group.GetAction() != contractv1.Change_ACTION_KEEP {
			return false
		}
	}
	return true
}

func ConfirmVerb(plan *contractv1.ChangePlan) string {
	counts := count(plan)
	if counts[contractv1.Change_ACTION_CREATE] > 0 && len(counts) == 1 {
		return "Create these"
	}
	return "Apply these changes"
}

func Tally(plan *contractv1.ChangePlan) string {
	counts := count(plan)
	var parts []string
	for _, action := range []contractv1.Change_Action{
		contractv1.Change_ACTION_CREATE,
		contractv1.Change_ACTION_UPDATE,
		contractv1.Change_ACTION_REPLACE,
		contractv1.Change_ACTION_DELETE,
	} {
		if n := counts[action]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d to %s", n, tallyVerb(action)))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ", ") + "."
}

func tallyVerb(action contractv1.Change_Action) string {
	switch action {
	case contractv1.Change_ACTION_CREATE:
		return "create"
	case contractv1.Change_ACTION_UPDATE:
		return "update"
	case contractv1.Change_ACTION_REPLACE:
		return "replace"
	default:
		return "delete"
	}
}

func count(plan *contractv1.ChangePlan) map[contractv1.Change_Action]int {
	counts := map[contractv1.Change_Action]int{}
	for _, group := range plan.GetGroups() {
		if group.GetAction() == contractv1.Change_ACTION_KEEP {
			continue
		}
		if len(group.GetChanges()) == 0 {
			tally(counts, group.GetAction())
			continue
		}
		for _, change := range group.GetChanges() {
			tally(counts, change.GetAction())
		}
	}
	return counts
}

func tally(counts map[contractv1.Change_Action]int, action contractv1.Change_Action) {
	switch action {
	case contractv1.Change_ACTION_CREATE,
		contractv1.Change_ACTION_UPDATE,
		contractv1.Change_ACTION_REPLACE,
		contractv1.Change_ACTION_DELETE:
		counts[action]++
	case contractv1.Change_ACTION_DISABLE_THEN_DELETE:
		counts[contractv1.Change_ACTION_DELETE]++
	}
}
