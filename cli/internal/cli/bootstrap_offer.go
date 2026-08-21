package cli

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/providerrunner"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

type bootstrapPlan struct {
	missing  []string
	stale    []string
	features []string
}

func planBootstrap(status *contractv1.BootstrapStatus) bootstrapPlan {
	var plan bootstrapPlan
	if !status.GetPresent() {
		return plan
	}
	for _, stack := range status.GetStacks() {
		feature := stack.GetFeature()
		switch {
		case stack.GetPresent():
			if stack.GetRequired() && !stack.GetDigestCurrent() {
				plan.stale = append(plan.stale, stack.GetName())
			}
			if feature != "" {
				plan.features = append(plan.features, feature)
			}
		case stack.GetRequired() && feature != "":
			plan.missing = append(plan.missing, feature)
			plan.features = append(plan.features, feature)
		}
	}
	slices.Sort(plan.features)
	return plan
}

func (p bootstrapPlan) empty() bool {
	return len(p.missing) == 0 && len(p.stale) == 0
}

func (p bootstrapPlan) summary() string {
	var parts []string
	if len(p.missing) > 0 {
		parts = append(parts, "add "+strings.Join(p.missing, ", "))
	}
	if len(p.stale) > 0 {
		parts = append(parts, "refresh "+strings.Join(p.stale, ", "))
	}
	return strings.Join(parts, " and ")
}

func (p bootstrapPlan) command(tier environmentv1.Tier) string {
	cmd := "ocel bootstrap"
	if tier == environmentv1.Tier_TIER_PREVIEW {
		cmd += " --preview"
	}
	if len(p.features) > 0 {
		cmd += " --features " + strings.Join(p.features, ",")
	}
	return cmd
}

func (p bootstrapPlan) refusal(tier environmentv1.Tier) error {
	if len(p.missing) == 0 {
		return nil
	}
	return fmt.Errorf(
		"the %s bootstrap does not carry what this project needs: %s.\nRun `%s` and try again",
		bootstrapName(tier), strings.Join(p.missing, ", "), p.command(tier),
	)
}

func (p bootstrapPlan) unattended(tier environmentv1.Tier, out io.Writer) error {
	if err := p.refusal(tier); err != nil {
		return err
	}
	fmt.Fprintf(out, "The %s bootstrap is behind what this build carries: %s.\nRun `%s` to refresh it.\n",
		bootstrapName(tier), strings.Join(p.stale, ", "), p.command(tier))
	return nil
}

func offerBootstrap(ctx context.Context, runner *providerrunner.Runner, status *contractv1.BootstrapStatus, tier environmentv1.Tier, interactive bool, out io.Writer, stdin io.Reader) error {
	plan := planBootstrap(status)
	if plan.empty() {
		return nil
	}
	if !interactive {
		return plan.unattended(tier, out)
	}

	fmt.Fprintf(out, "The %s bootstrap is not what this project needs: %s.\n", bootstrapName(tier), plan.summary())
	proceed, err := confirmYN(ctx, fmt.Sprintf("Run `%s` now?", plan.command(tier)), out, stdin)
	if err != nil {
		return err
	}
	if !proceed {
		return plan.unattended(tier, out)
	}
	req := &contractv1.BootstrapRequest{
		Tier:     tier,
		Features: plan.features,
	}
	return providerrunner.Stream(ctx, runner, "Bootstrap", req, contractv1connect.ProviderServiceClient.Bootstrap,
		func(ev *progressv1.OperationEvent) { reportBootstrapEvent(out, ev) })
}

func reportBootstrapEvent(out io.Writer, ev *progressv1.OperationEvent) {
	switch {
	case ev.GetProgress() != nil:
		fmt.Fprintf(out, "  %s\n", ev.GetProgress().GetMessage())
	case ev.GetLog() != nil:
		fmt.Fprintf(out, "  %s\n", ev.GetLog().GetMessage())
	}
}
