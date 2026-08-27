package bootstrap

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/prompt"
	"github.com/ocelhq/ocel/cli/internal/provider"
	"github.com/ocelhq/ocel/cli/internal/runui"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

type Plan struct {
	Missing  []string
	Stale    []string
	Features []string
}

func PlanFor(status *contractv1.BootstrapStatus) Plan {
	var plan Plan
	if !status.GetPresent() {
		return plan
	}
	for _, stack := range status.GetStacks() {
		feature := stack.GetFeature()
		switch {
		case stack.GetPresent():
			if stack.GetRequired() && !stack.GetDigestCurrent() {
				plan.Stale = append(plan.Stale, stack.GetName())
			}
			if feature != "" {
				plan.Features = append(plan.Features, feature)
			}
		case stack.GetRequired() && feature != "":
			plan.Missing = append(plan.Missing, feature)
			plan.Features = append(plan.Features, feature)
		}
	}
	slices.Sort(plan.Features)
	return plan
}

func (p Plan) Empty() bool {
	return len(p.Missing) == 0 && len(p.Stale) == 0
}

func (p Plan) summary() string {
	var parts []string
	if len(p.Missing) > 0 {
		parts = append(parts, "add "+strings.Join(p.Missing, ", "))
	}
	if len(p.Stale) > 0 {
		parts = append(parts, "refresh "+strings.Join(p.Stale, ", "))
	}
	return strings.Join(parts, " and ")
}

func (p Plan) Command(tier environmentv1.Tier) string {
	cmd := "ocel bootstrap " + Name(tier)
	if len(p.Features) > 0 {
		cmd += " --features " + strings.Join(p.Features, ",")
	}
	return cmd
}

func (p Plan) Request(tier environmentv1.Tier, front *contractv1.EdgeSelection) *contractv1.BootstrapRequest {
	return &contractv1.BootstrapRequest{
		Tier:     tier,
		Features: p.Features,
		Edge:     front,
	}
}

func (p Plan) Refusal(tier environmentv1.Tier) error {
	if len(p.Missing) == 0 {
		return nil
	}
	return fmt.Errorf(
		"the %s bootstrap does not carry what this project needs: %s.\nRun `%s` and try again",
		Name(tier), strings.Join(p.Missing, ", "), p.Command(tier),
	)
}

func (p Plan) Insist(tier environmentv1.Tier) error {
	if err := p.Refusal(tier); err != nil {
		return err
	}
	if len(p.Stale) == 0 {
		return nil
	}
	return fmt.Errorf(
		"the %s bootstrap is behind what this build has: %s.\nRun `%s` and try again",
		Name(tier), strings.Join(p.Stale, ", "), p.Command(tier),
	)
}

func (p Plan) Advise(tier environmentv1.Tier, rep runui.Reporter) error {
	if err := p.Refusal(tier); err != nil {
		return err
	}
	rep.Warning(fmt.Sprintf("The %s bootstrap is behind what this build has: %s.\nRun `%s` to refresh it.",
		Name(tier), strings.Join(p.Stale, ", "), p.Command(tier)))
	return nil
}

func Offer(ctx context.Context, runner *provider.Runner, status *contractv1.BootstrapStatus, tier environmentv1.Tier, front *contractv1.EdgeSelection, rep runui.Reporter, interactive bool, out io.Writer, in io.Reader) error {
	plan := PlanFor(status)
	if plan.Empty() {
		return nil
	}
	if !interactive {
		return plan.Advise(tier, rep)
	}

	rep.Warning(fmt.Sprintf("The %s bootstrap is not what this project needs: %s.", Name(tier), plan.summary()))
	proceed, err := confirmHealing(ctx, plan, tier, rep, out, in)
	if err != nil {
		return err
	}
	if !proceed {
		return plan.Advise(tier, rep)
	}
	return provider.Stream(ctx, runner, "Bootstrap", plan.Request(tier, front), contractv1connect.ProviderServiceClient.Bootstrap,
		func(ev *progressv1.OperationEvent) { reportEvent(rep, ev) })
}

func confirmHealing(ctx context.Context, plan Plan, tier environmentv1.Tier, rep runui.Reporter, out io.Writer, in io.Reader) (bool, error) {
	resume := rep.Suspend()
	defer resume()
	return prompt.New(out, in).Confirm(ctx, fmt.Sprintf("Run `%s` now?", plan.Command(tier)))
}

func reportEvent(rep runui.Reporter, ev *progressv1.OperationEvent) {
	switch {
	case ev.GetProgress() != nil:
		rep.Diagnostic("  " + ev.GetProgress().GetMessage())
	case ev.GetLog() != nil:
		rep.Diagnostic("  " + ev.GetLog().GetMessage())
	}
}
