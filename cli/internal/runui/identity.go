package runui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

const (
	identityRule = "▎"
	identityGap  = "  "
	identityName = "ocel"
	accentColor  = 6
)

func IdentityBlock(present Presentation, ev *streamv1.IdentityEvent) []string {
	accent, faint := identityStyles(present)
	rule := accent.Render(identityRule) + " "

	head := identityHeadline(ev)
	width := vendorWidth(ev)
	rows := []string{
		identityRow(rule, faint, width, ev.GetOrigin().GetVendor(), originValues(ev.GetOrigin())),
		identityRow(rule, faint, width, ev.GetEdge().GetVendor(), []string{ev.GetEdge().GetAccount()}),
	}

	lines := make([]string, 0, len(rows)+2)
	for _, row := range rows {
		if row != "" {
			lines = append(lines, row)
		}
	}
	if head == "" && len(lines) == 0 {
		return nil
	}
	return append(append([]string{rule + accent.Render(identityName) + head}, lines...), "")
}

func identityStyles(present Presentation) (accent, faint lipgloss.Style) {
	if !present.Color {
		return lipgloss.NewStyle(), lipgloss.NewStyle()
	}
	return lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(accentColor)), lipgloss.NewStyle().Faint(true)
}

func identityHeadline(ev *streamv1.IdentityEvent) string {
	named := nonEmpty(ev.GetProject(), tierName(ev.GetTier()))
	if len(named) == 0 {
		return ""
	}
	return identityGap + strings.Join(named, pathSep)
}

func vendorWidth(ev *streamv1.IdentityEvent) int {
	width := ansi.StringWidth(identityName)
	for _, vendor := range []string{ev.GetOrigin().GetVendor(), ev.GetEdge().GetVendor()} {
		if w := ansi.StringWidth(vendor); w > width {
			width = w
		}
	}
	return width
}

func identityRow(rule string, faint lipgloss.Style, width int, vendor string, values []string) string {
	values = nonEmpty(values...)
	if vendor == "" && len(values) == 0 {
		return ""
	}
	pad := strings.Repeat(" ", width-ansi.StringWidth(vendor)) + identityGap
	return rule + faint.Render(vendor) + pad + strings.Join(values, identityGap)
}

func originValues(party *streamv1.Party) []string {
	account, principal := party.GetAccount(), party.GetPrincipal()
	if party.GetLocation() == "" && account != "" && principal != "" {
		return []string{principal + "@" + account}
	}
	return []string{account, principal, party.GetLocation()}
}

func nonEmpty(values ...string) []string {
	kept := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			kept = append(kept, value)
		}
	}
	return kept
}

func tierName(tier environmentv1.Tier) string {
	switch tier {
	case environmentv1.Tier_TIER_PREVIEW:
		return "preview"
	case environmentv1.Tier_TIER_PRODUCTION:
		return "production"
	default:
		return ""
	}
}
