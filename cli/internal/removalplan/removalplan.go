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

func Print(out io.Writer, header string, plan *contractv1.RemovalPlan, footer ...string) {
	if kind := plan.GetEdgeKind(); kind != "" {
		header += fmt.Sprintf(", fronted by the %s edge", kind)
	}
	fmt.Fprintf(out, "%s:\n", header)

	kept := printItems(out, plan.GetItems())
	for _, line := range footer {
		fmt.Fprintln(out, line)
	}
	printKept(out, kept)
}

func printItems(out io.Writer, items []*contractv1.RemovalItem) []*contractv1.RemovalItem {
	var kept []*contractv1.RemovalItem
	for _, item := range items {
		if item.GetAction() == contractv1.RemovalItem_ACTION_KEEP {
			kept = append(kept, item)
			continue
		}
		fmt.Fprintf(out, "  • %s\n", ItemLine(item))
	}
	return kept
}

func printKept(out io.Writer, kept []*contractv1.RemovalItem) {
	if len(kept) == 0 {
		return
	}
	fmt.Fprintln(out, "Left in place:")
	for _, item := range kept {
		fmt.Fprintf(out, "  • %s\n", ItemLine(item))
	}
}

func ItemLine(item *contractv1.RemovalItem) string {
	line := fmt.Sprintf("%s %s %s", itemAction(item.GetAction()), item.GetKind(), item.GetName())
	if reason := item.GetReason(); reason != "" {
		line += " — " + reason
	}
	if item.GetSlow() {
		line += " (this one is slow)"
	}
	return line
}

func itemAction(action contractv1.RemovalItem_Action) string {
	switch action {
	case contractv1.RemovalItem_ACTION_DELETE:
		return "delete"
	case contractv1.RemovalItem_ACTION_DISABLE_THEN_DELETE:
		return "disable, then delete"
	case contractv1.RemovalItem_ACTION_KEEP:
		return "keep"
	default:
		return fmt.Sprintf("act on (%s, an action this CLI does not know)", action)
	}
}
