package edge

import "context"

type Hooks struct {
	PlanBootstrap         func(ctx context.Context, class Class) ([]PlanChange, error)
	PlanRemoveBootstrap   func(ctx context.Context, class Class) ([]PlanChange, error)
	Adoption              func(ctx context.Context, class Class) (Adoption, error)
	BootstrapStands       func(ctx context.Context, class Class) (bool, error)
	BoundHostnames        func(ctx context.Context, class Class) ([]string, error)
	VerifyCredentials     func(ctx context.Context) (CredentialIdentity, error)
	CodeEntitlement       func(ctx context.Context) (CodeEntitlement, error)
	CredentialPermissions func(tier CredentialTier) (CredentialDocument, error)
	Compatibility         func() (compatDate string, compatFlags []string)
}
