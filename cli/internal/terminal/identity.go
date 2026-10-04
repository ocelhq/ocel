package terminal

import (
	"cmp"
	"strings"
	"unicode/utf8"

	"github.com/ocelhq/ocel/cli/internal/version"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

const (
	identityGap  = "  "
	identityName = "ocel"
	pathSep      = " › "
	partySep     = " · "
)

func identityLines(present Presentation, ev *streamv1.IdentityEvent) []string {
	var out []string
	if head := identityHeadline(present, ev); head != "" {
		out = append(out, present.Palette().pill().Render(identityName)+identityGap+present.Palette().Muted(version.Version)+head)
	}
	rows := partyRows(present, ev)
	if len(out) > 0 && len(rows) > 0 {
		out = append(out, "")
	}
	return append(out, rows...)
}

func partyRows(present Presentation, ev *streamv1.IdentityEvent) []string {
	var parties []*streamv1.Party
	width := 0
	for _, party := range []*streamv1.Party{ev.GetOrigin(), ev.GetEdge()} {
		if isNamedParty(party) {
			parties = append(parties, party)
			width = max(width, utf8.RuneCountInString(vendorName(party)))
		}
	}
	rows := make([]string, 0, len(parties))
	for _, party := range parties {
		vendor := vendorName(party)
		row := blockIndent + present.Palette().Bold(vendor)
		if detail := partyDetail(present, party); detail != "" {
			row += strings.Repeat(" ", width-utf8.RuneCountInString(vendor)) + identityGap + detail
		}
		rows = append(rows, row)
	}
	return rows
}

func targetLine(present Presentation, party *streamv1.Party) string {
	if !isNamedParty(party) {
		return ""
	}
	text := blockIndent + present.Palette().Muted("on ") + present.Palette().Bold(vendorName(party))
	if detail := partyDetail(present, party); detail != "" {
		text += " " + detail
	}
	return text
}

func partyDetail(present Presentation, party *streamv1.Party) string {
	parts := nonEmpty(party.GetAccount(), party.GetLocation())
	if principal := party.GetPrincipal(); principal != "" {
		parts = append(parts, present.Palette().Muted("as ")+principal)
	}
	return strings.Join(parts, present.Palette().Muted(partySep))
}

func isNamedParty(party *streamv1.Party) bool {
	return party.GetVendor()+party.GetPrincipal()+party.GetAccount()+party.GetLocation() != ""
}

func vendorName(party *streamv1.Party) string {
	return cmp.Or(party.GetVendor(), "your provider")
}

func identityHeadline(present Presentation, ev *streamv1.IdentityEvent) string {
	var named []string
	if project := ev.GetProject(); project != "" {
		named = append(named, present.Palette().Bold(project))
	}
	if tier := tierName(ev.GetTier()); tier != "" {
		if ev.GetTier() == environmentv1.Tier_TIER_PRODUCTION {
			tier = present.Palette().WarningBold(tier)
		}
		named = append(named, tier)
	}
	if len(named) == 0 {
		return ""
	}
	return identityGap + strings.Join(named, present.Palette().Muted(pathSep))
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
