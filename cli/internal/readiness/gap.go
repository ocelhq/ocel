package readiness

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/prerequisite"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/run"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

type Gap struct {
	Missing    []string
	Stale      []string
	Unreadable []string
	Features   []string
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
			switch {
			case !stack.GetRequired():
			case stack.GetReadError() != "":
				gap.Unreadable = append(gap.Unreadable, stack.GetName()+" ("+stack.GetReadError()+")")
			case !stack.GetDigestCurrent():
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
	gap.Features = slices.Compact(gap.Features)
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
	return MissingFeaturesError{Gap: g, Tier: tier}.refuse()
}

type MissingFeaturesError struct {
	Gap      Gap
	Tier     environmentv1.Tier
	Edge     *contractv1.EdgeSelection
	Provider *providerprocess.Provider
}

func (e MissingFeaturesError) Error() string {
	return fmt.Sprintf(
		"the %s bootstrap does not include what this project needs: %s.\nRun `%s` and try again",
		TierName(e.Tier), strings.Join(e.Gap.Missing, ", "), e.Gap.RepairCommand(e.Tier),
	)
}

func (MissingFeaturesError) Missing() prerequisite.Kind { return prerequisite.Bootstrap }

func (e MissingFeaturesError) Finding() string {
	return fmt.Sprintf("The %s bootstrap does not include what this project needs: %s.", TierName(e.Tier), strings.Join(e.Gap.Missing, ", "))
}

func (e MissingFeaturesError) Remedy() string { return "`" + e.Hint() + "`" }

func (e MissingFeaturesError) Hint() string { return e.Gap.RepairCommand(e.Tier) }

func (e MissingFeaturesError) refuse() error {
	return &clierror.Error{Code: clierror.CodeBootstrapFeaturesMissing, Hint: e.Hint(), Cause: e}
}

func (e MissingFeaturesError) BootstrapRequest() *contractv1.BootstrapRequest {
	return e.Gap.BootstrapRequest(e.Tier, e.Edge)
}

func (e MissingFeaturesError) BootstrapProvider() *providerprocess.Provider { return e.Provider }

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

func (g Gap) WarnUnreadable(tier environmentv1.Tier, span *run.Span) {
	if len(g.Unreadable) == 0 {
		return
	}
	span.Warn(fmt.Sprintf("Could not read part of the %s bootstrap, so whether it is current is unknown: %s.",
		TierName(tier), strings.Join(g.Unreadable, ", ")))
}

func (g Gap) WarnStale(tier environmentv1.Tier, span *run.Span) {
	if len(g.Stale) == 0 {
		return
	}
	span.Warn(fmt.Sprintf("The %s bootstrap is behind what this build has: %s.\nRun `%s` to refresh it.",
		TierName(tier), strings.Join(g.Stale, ", "), g.RepairCommand(tier)))
}
