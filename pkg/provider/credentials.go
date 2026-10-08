package provider

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
)

type Credentials interface {
	Whoami(ctx context.Context) (Principal, error)

	Permissions(purpose edge.CredentialPurpose, tier environment.Tier) (edge.CredentialDocument, error)
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
