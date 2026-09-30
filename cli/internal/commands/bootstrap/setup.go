package bootstrap

import (
	"context"
	"errors"
	"fmt"

	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/prerequisite"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/readiness"
	"github.com/ocelhq/ocel/cli/internal/run"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

func NewSetup() prerequisite.Setup {
	return prerequisite.Setup{
		Prompt:         func(missing prerequisite.MissingError) string { return fmt.Sprintf("Run %s now?", missing.Remedy()) },
		ChangesAccount: true,
		Run:            runSetup,
	}
}

func runSetup(ctx context.Context, _ consent.Policy, span *run.Span, missing prerequisite.MissingError) error {
	provider, req := bootstrapFor(missing)
	if provider == nil {
		return missing
	}
	if _, err := providerprocess.Stream(ctx, provider, "Bootstrap", req, contractv1connect.ProviderServiceClient.Bootstrap); err != nil {
		return err
	}
	span.Say(fmt.Sprintf("Bootstrapped %s", readiness.TierName(req.GetTier())))
	return nil
}

func bootstrapFor(missing prerequisite.MissingError) (*providerprocess.Provider, *contractv1.BootstrapRequest) {
	var absent readiness.NoInfrastructureError
	if errors.As(missing, &absent) {
		return absent.Provider, absent.BootstrapRequest()
	}
	var lacking readiness.MissingFeaturesError
	if errors.As(missing, &lacking) {
		return lacking.Provider, lacking.BootstrapRequest()
	}
	return nil, nil
}
