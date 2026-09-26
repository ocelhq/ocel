package provider

import (
	"context"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Credentials interface {
	Whoami(ctx context.Context) (Principal, error)

	Permissions(tier edge.CredentialTier) (edge.CredentialDocument, error)
}

type Principal struct {
	Vendor    Vendor
	Account   string
	Name      string
	Location  string
	EdgeScope string
	Details   []PrincipalDetail
}

type PrincipalDetail struct {
	Label string
	Value string
}
