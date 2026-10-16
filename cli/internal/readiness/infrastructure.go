package readiness

import (
	"fmt"

	"github.com/ocelhq/ocel/cli/internal/prerequisite"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

type MissingBootstrapError interface {
	prerequisite.MissingError
	BootstrapRequest() *contractv1.BootstrapRequest
	BootstrapProvider() *providerprocess.Provider
}

type NoInfrastructureError struct {
	Tier        environmentv1.Tier
	PresentTier environmentv1.Tier
	Account     string
	Edge        *contractv1.EdgeSelection
	Provider    *providerprocess.Provider
}

func (e NoInfrastructureError) Error() string {
	if e.PresentTier != environmentv1.Tier_TIER_UNSPECIFIED {
		return fmt.Sprintf("this command needs %s infrastructure, but the account points at %s.\nRun `%s` and try again",
			TierName(e.Tier), infraLabel(e.PresentTier), e.command())
	}
	return fmt.Sprintf("no %s infrastructure is set up yet.\nRun `%s` and try again", TierName(e.Tier), e.command())
}

func (NoInfrastructureError) Missing() prerequisite.Kind { return prerequisite.Bootstrap }

func (e NoInfrastructureError) Finding() string {
	account := e.Account
	if account == "" {
		account = "This account"
	}
	return fmt.Sprintf("%s has no Ocel %s infrastructure yet. Setting it up is a one-time step.", account, TierName(e.Tier))
}

func (e NoInfrastructureError) Remedy() string { return "`" + e.Hint() + "`" }

func (e NoInfrastructureError) Hint() string { return e.command() }

func (e NoInfrastructureError) BootstrapRequest() *contractv1.BootstrapRequest {
	return &contractv1.BootstrapRequest{Tier: e.Tier, Edge: e.Edge}
}

func (e NoInfrastructureError) BootstrapProvider() *providerprocess.Provider { return e.Provider }

func (e NoInfrastructureError) command() string {
	return BootstrapCommand(e.Tier)
}
