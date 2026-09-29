package readiness

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/run"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

type Gap struct {
	Missing  []string
	Stale    []string
	Features []string
}

func NewGap(status *contractv1.BootstrapStatus) Gap {
	var gap Gap
	if !status.GetPresent() {
		return gap
	}
	for _, stack := range status.GetStacks() {
		feature := stack.GetFeature()
		switch {
		case stack.GetPresent():
			if stack.GetRequired() && !stack.GetDigestCurrent() {
				gap.Stale = append(gap.Stale, stack.GetName())
			}
			if feature != "" {
				gap.Features = append(gap.Features, feature)
			}
		case stack.GetRequired() && feature != "":
			gap.Missing = append(gap.Missing, feature)
			gap.Features = append(gap.Features, feature)
		}
	}
	slices.Sort(gap.Features)
	return gap
}

func NewFeatureGap(status *contractv1.BootstrapStatus, feature string) Gap {
	if !status.GetPresent() {
		return Gap{}
	}
	gap := Gap{Features: []string{feature}}
	for _, stack := range status.GetStacks() {
		if stack.GetFeature() == feature && stack.GetPresent() {
			return gap
		}
	}
	gap.Missing = []string{feature}
	return gap
}

func (g Gap) IsEmpty() bool {
	return len(g.Missing) == 0 && len(g.Stale) == 0
}

func (g Gap) RepairCommand(tier environmentv1.Tier) string {
	command := BootstrapCommand(tier)
	if len(g.Features) > 0 {
		command += " --features " + strings.Join(g.Features, ",")
	}
	return command
}

func (g Gap) BootstrapRequest(tier environmentv1.Tier, edge *contractv1.EdgeSelection) *contractv1.BootstrapRequest {
	return &contractv1.BootstrapRequest{
		Tier:     tier,
		Features: g.Features,
		Edge:     edge,
	}
}

func (g Gap) RefuseMissing(tier environmentv1.Tier) error {
	if len(g.Missing) == 0 {
		return nil
	}
	return fmt.Errorf(
		"the %s bootstrap does not include what this project needs: %s.\nRun `%s` and try again",
		TierName(tier), strings.Join(g.Missing, ", "), g.RepairCommand(tier),
	)
}

func (g Gap) RefuseIncomplete(tier environmentv1.Tier) error {
	if err := g.RefuseMissing(tier); err != nil {
		return err
	}
	if len(g.Stale) == 0 {
		return nil
	}
	return fmt.Errorf(
		"the %s bootstrap is behind what this build has: %s.\nRun `%s` and try again",
		TierName(tier), strings.Join(g.Stale, ", "), g.RepairCommand(tier),
	)
}

func (g Gap) refuseMissingWarnStale(tier environmentv1.Tier, span *run.Span) error {
	if err := g.RefuseMissing(tier); err != nil {
		return err
	}
	span.Warn(fmt.Sprintf("The %s bootstrap is behind what this build has: %s.\nRun `%s` to refresh it.",
		TierName(tier), strings.Join(g.Stale, ", "), g.RepairCommand(tier)))
	return nil
}

func (g Gap) summary() string {
	var parts []string
	if len(g.Missing) > 0 {
		parts = append(parts, "add "+strings.Join(g.Missing, ", "))
	}
	if len(g.Stale) > 0 {
		parts = append(parts, "refresh "+strings.Join(g.Stale, ", "))
	}
	return strings.Join(parts, " and ")
}
