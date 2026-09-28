package edge

import "context"

type Hooks struct {
	PlanBootstrap                 func(ctx context.Context, class Class) ([]PlanChange, error)
	PlanRemoveBootstrap           func(ctx context.Context, class Class) ([]PlanChange, error)
	PlanAdoption                  func(ctx context.Context, class Class) (Adoption, error)
	CheckBootstrapInstalled       func(ctx context.Context, class Class) (bool, error)
	ListBoundHostnames            func(ctx context.Context, class Class) ([]string, error)
	VerifyCredentials             func(ctx context.Context) (CredentialIdentity, error)
	CheckCodeEntitlement          func(ctx context.Context) (CodeEntitlement, error)
	DescribeCredentialPermissions func(tier CredentialTier) (CredentialDocument, error)
}
