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
	ClientCertificates            *ClientCertificateHooks
	OriginCertificates            *OriginCertificateHooks
	PurgeHostnames                func(ctx context.Context, hostnames []string) error
}

type ClientCertificateHooks struct {
	Ensure  func(ctx context.Context, hostname string) ([]string, error)
	Present func(ctx context.Context, hostname string) error
}

type OriginCertificateHooks struct {
	Issue  func(ctx context.Context, hostname string) (OriginCertificate, error)
	Revoke func(ctx context.Context, id string) error
}
