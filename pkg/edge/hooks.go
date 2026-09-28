package edge

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
)

type Hooks struct {
	PlanBootstrap                 func(ctx context.Context, tier environment.Tier) ([]PlanChange, error)
	PlanRemoveBootstrap           func(ctx context.Context, tier environment.Tier) ([]PlanChange, error)
	PlanAdoption                  func(ctx context.Context, tier environment.Tier) (Adoption, error)
	CheckBootstrapInstalled       func(ctx context.Context, tier environment.Tier) (bool, error)
	ListBoundHostnames            func(ctx context.Context, tier environment.Tier) ([]string, error)
	VerifyCredentials             func(ctx context.Context) (CredentialIdentity, error)
	CheckCodeEntitlement          func(ctx context.Context) (CodeEntitlement, error)
	DescribeCredentialPermissions func(purpose CredentialPurpose) (CredentialDocument, error)
}
