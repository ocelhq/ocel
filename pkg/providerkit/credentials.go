package providerkit

import "context"

type Credentials interface {
	Whoami(ctx context.Context) (Identity, error)

	Policy(tier CredentialTier) (string, error)
}

type CredentialTier string

const (
	TierBootstrap CredentialTier = "bootstrap"
	TierDeploy    CredentialTier = "deploy"
)

type Identity struct {
	Provider  Vendor
	Account   string
	Principal string
	EdgeScope string
	Details   []Detail
}

type Detail struct {
	Label string
	Value string
}
