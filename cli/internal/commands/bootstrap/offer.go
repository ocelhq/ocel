package bootstrap

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/preflight"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
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
		"the %s bootstrap does not include what this project needs: %s.\nRun `%s` and try again",
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

func (p Plan) Advise(tier environmentv1.Tier, span *run.Span) error {
	if err := p.Refusal(tier); err != nil {
		return err
	}
	span.Warn(fmt.Sprintf("The %s bootstrap is behind what this build has: %s.\nRun `%s` to refresh it.",
		Name(tier), strings.Join(p.Stale, ", "), p.Command(tier)))
	return nil
}

func Offers(ctx context.Context, prov *providerprocess.Provider, tier environmentv1.Tier, front *contractv1.EdgeSelection, feature string) (bool, error) {
	var described *contractv1.DescribeBootstrapResponse
	err := prov.Call(ctx, func(client contractv1connect.ProviderServiceClient) error {
		var err error
		described, err = client.DescribeBootstrap(ctx, &contractv1.DescribeBootstrapRequest{Tier: tier, Edge: front})
		return err
	})
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(described.GetFeatures(), func(f *contractv1.Feature) bool {
		return f.GetName() == feature
	}), nil
}

func PlanOnly(status *contractv1.BootstrapStatus, feature string) Plan {
	if !status.GetPresent() {
		return Plan{}
	}
	plan := Plan{Features: []string{feature}}
	for _, stack := range status.GetStacks() {
		if stack.GetFeature() == feature && stack.GetPresent() {
			return plan
		}
	}
	plan.Missing = []string{feature}
	return plan
}

func Offer(ctx context.Context, span *run.Span, prov *providerprocess.Provider, status *contractv1.BootstrapStatus, tier environmentv1.Tier, front *contractv1.EdgeSelection, interactive bool, out io.Writer, in io.Reader) error {
	return OfferPlan(ctx, span, prov, PlanFor(status), tier, front, interactive, out, in)
}

func OfferPlan(ctx context.Context, span *run.Span, prov *providerprocess.Provider, plan Plan, tier environmentv1.Tier, front *contractv1.EdgeSelection, interactive bool, out io.Writer, in io.Reader) error {
	if plan.Empty() {
		return nil
	}
	if !interactive {
		return plan.Advise(tier, span)
	}

	span.Warn(fmt.Sprintf("The %s bootstrap is not what this project needs: %s.", Name(tier), plan.summary()))
	proceed, err := confirmRepair(ctx, plan, tier, span, out, in)
	if err != nil {
		return err
	}
	if !proceed {
		return plan.Advise(tier, span)
	}
	_, err = providerprocess.Stream(ctx, prov, "Bootstrap", plan.Request(tier, front), contractv1connect.ProviderServiceClient.Bootstrap)
	return err
}

func confirmRepair(ctx context.Context, plan Plan, tier environmentv1.Tier, span *run.Span, out io.Writer, in io.Reader) (bool, error) {
	return span.Confirm(func() (bool, error) {
		return terminal.NewPrompt(out, in).Confirm(ctx, fmt.Sprintf("Run `%s` now?", plan.Command(tier)))
	})
}

func Ready(ctx context.Context, span *run.Span, prov *providerprocess.Provider, cfg *project.Project, required environmentv1.Tier, hint string) error {
	resp, err := preflight.Run(ctx, span, prov, cfg, required, "", nil, preflight.Frameworks(cfg), hint)
	if err != nil {
		return err
	}
	return PlanFor(resp.GetBootstrap()).Refusal(required)
}
