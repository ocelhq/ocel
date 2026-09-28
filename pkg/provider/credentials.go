package provider

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
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
