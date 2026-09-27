package runui

import (
	"cmp"
	"strings"

	"charm.land/lipgloss/v2"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

var Version = "dev"

const (
	identityGap  = "  "
	identityName = "ocel"
	pathSep      = " › "
	accentColor  = 6
	pillText     = 0
)

func identityLines(present Presentation, ev *streamv1.IdentityEvent) []string {
	head := identityHeadline(ev)
	if head == "" {
		return nil
	}
	pill, faint := identityStyles(present)
	return []string{pill.Render(identityName) + identityGap + faint.Render(Version) + head}
}

func signedIn(ev *streamv1.IdentityEvent) string {
	var parties []string
	for _, party := range []*streamv1.Party{ev.GetOrigin(), ev.GetEdge()} {
		if party.GetVendor()+party.GetPrincipal()+party.GetAccount()+party.GetLocation() == "" {
			continue
		}
		text := cmp.Or(party.GetVendor(), "your provider")
		if principal := party.GetPrincipal(); principal != "" {
			text += " as " + principal
		}
		if where := nonEmpty(party.GetAccount(), party.GetLocation()); len(where) > 0 {
			text += " (" + strings.Join(where, ", ") + ")"
		}
		parties = append(parties, text)
	}
	if len(parties) == 0 {
		return ""
	}
	return "Signed in to " + strings.Join(parties, " and ")
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
