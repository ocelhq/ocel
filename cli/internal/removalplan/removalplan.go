package removalplan

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

	kept := printGroups(out, plan.GetGroups())
	for _, line := range footer {
		fmt.Fprintln(out, line)
	}
	printKept(out, kept)
}

func printGroups(out io.Writer, groups []*contractv1.ChangeGroup) []*contractv1.ChangeGroup {
	var kept []*contractv1.ChangeGroup
	for _, group := range groups {
		if group.GetAction() == contractv1.Change_ACTION_KEEP {
			kept = append(kept, group)
			continue
		}
		fmt.Fprintf(out, "  • %s\n", GroupLine(group))
	}
	return kept
}

func printKept(out io.Writer, kept []*contractv1.ChangeGroup) {
	if len(kept) == 0 {
		return
	}
	fmt.Fprintln(out, "Left in place:")
	for _, group := range kept {
		fmt.Fprintf(out, "  • %s\n", GroupLine(group))
	}
}

func GroupLine(group *contractv1.ChangeGroup) string {
	line := fmt.Sprintf("%s %s %s", groupAction(group.GetAction()), group.GetKind(), group.GetName())
	if reason := group.GetReason(); reason != "" {
		line += " — " + reason
	}
	if group.GetSlow() {
		line += " (this one is slow)"
	}
	return line
}

func groupAction(action contractv1.Change_Action) string {
	switch action {
	case contractv1.Change_ACTION_DELETE:
		return "delete"
	case contractv1.Change_ACTION_DISABLE_THEN_DELETE:
		return "disable, then delete"
	case contractv1.Change_ACTION_KEEP:
		return "keep"
	default:
		return fmt.Sprintf("act on (%s, an action this CLI does not know)", action)
	}
}
