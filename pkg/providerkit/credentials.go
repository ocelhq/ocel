package providerkit

import (
	"context"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Credentials interface {
	Whoami(ctx context.Context) (Identity, error)

	Permissions(tier CredentialTier) (edge.CredentialDocument, error)
}

type CredentialTier = edge.CredentialTier

const (
	TierBootstrap = edge.TierBootstrap
	TierDeploy    = edge.TierDeploy
)

type Identity struct {
	Provider  Vendor
	Account   string
	Principal string
	Location  string
	EdgeScope string
	Details   []Detail
}

type Detail struct {
	Label string
	Value string
}
