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
	var incomplete readiness.MissingBootstrapError
	if !errors.As(missing, &incomplete) {
		return missing
	}
	req := incomplete.BootstrapRequest()
	if _, err := providerprocess.Stream(ctx, incomplete.BootstrapProvider(), "Bootstrap", req, contractv1connect.ProviderServiceClient.Bootstrap); err != nil {
		return err
	}
	span.Say(fmt.Sprintf("Bootstrapped %s", readiness.TierName(req.GetTier())))
	return nil
}
