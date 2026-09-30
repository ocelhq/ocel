package bootstrap

import (
	"context"
	"errors"
	"fmt"

	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/prerequisite"
	"github.com/ocelhq/ocel/cli/internal/readiness"
	"github.com/ocelhq/ocel/cli/internal/run"
)

func NewSetup() prerequisite.Setup {
	return prerequisite.Setup{
		Prompt:         func(missing prerequisite.MissingError) string { return fmt.Sprintf("Run %s now?", missing.Remedy()) },
		ChangesAccount: true,
		Run:            runSetup,
	}
}

func runSetup(ctx context.Context, policy consent.Policy, span *run.Span, missing prerequisite.MissingError) error {
	var incomplete readiness.MissingBootstrapError
	if !errors.As(missing, &incomplete) {
		return missing
	}
	req, provider := incomplete.BootstrapRequest(), incomplete.BootstrapProvider()
	drawn, err := drawPlan(ctx, span, provider, req)
	if err != nil {
		return err
	}
	granted, err := confirmPlan(ctx, policy, span, provider, req.GetTier(), drawn)
	if err != nil {
		return err
	}
	if !granted {
		return prerequisite.SetupDeclinedError{Missing: missing}
	}
	if err := applyPlan(ctx, provider, req, drawn); err != nil {
		return err
	}
	span.Say(fmt.Sprintf("Bootstrapped %s", readiness.TierName(req.GetTier())))
	return nil
}
