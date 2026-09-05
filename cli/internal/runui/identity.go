package runui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

var Version = "dev"

const (
	identityGap  = "  "
	identityName = "ocel"
	accentColor  = 6
	pillText     = 0
)

func IdentityBlock(present Presentation, ev *streamv1.IdentityEvent) []string {
	pill, faint := identityStyles(present)

	head := identityHeadline(ev)
	width := vendorWidth(ev)
	rows := nonEmpty(
		identityRow(faint, width, ev.GetOrigin().GetVendor(), originValues(ev.GetOrigin())),
		identityRow(faint, width, ev.GetEdge().GetVendor(), []string{ev.GetEdge().GetAccount()}),
	)
	if head == "" && len(rows) == 0 {
		return nil
	}

	lines := []string{"", pill.Render(identityName) + identityGap + faint.Render(Version) + head}
	if len(rows) > 0 {
		lines = append(append(lines, ""), rows...)
	}
	return append(lines, "")
}

func identityStyles(present Presentation) (pill, faint lipgloss.Style) {
	if !present.Color {
		return lipgloss.NewStyle(), lipgloss.NewStyle()
	}
	pill = lipgloss.NewStyle().
		Background(lipgloss.ANSIColor(accentColor)).
		Foreground(lipgloss.ANSIColor(pillText)).
		Bold(true).
		Padding(0, 1)
	return pill, lipgloss.NewStyle().Faint(true)
}

func identityHeadline(ev *streamv1.IdentityEvent) string {
	named := nonEmpty(ev.GetProject(), tierName(ev.GetTier()))
	if len(named) == 0 {
		return ""
	}
	return identityGap + strings.Join(named, pathSep)
}

func vendorWidth(ev *streamv1.IdentityEvent) int {
	width := 0
	for _, vendor := range []string{ev.GetOrigin().GetVendor(), ev.GetEdge().GetVendor()} {
		if w := ansi.StringWidth(vendor); w > width {
			width = w
		}
	}
	return width
}

func identityRow(faint lipgloss.Style, width int, vendor string, values []string) string {
	values = nonEmpty(values...)
	if vendor == "" && len(values) == 0 {
		return ""
	}
	pad := strings.Repeat(" ", width-ansi.StringWidth(vendor)) + identityGap
	return faint.Render(vendor) + pad + strings.Join(values, identityGap)
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
