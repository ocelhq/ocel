package providerkit

import "context"

// Credentials answers who we are acting as and whether that is enough. The kit
// decides what to do about the answer — refuse, warn, or print the policy — and
// a vendor never renders a message.
type Credentials interface {
	// Whoami identifies the caller. It is the one call every command makes before
	// anything else, so it must be cheap and must not need a bootstrap.
	Whoami(ctx context.Context) (Identity, error)

	// Problems lists what the current credentials cannot do at this tier. An
	// empty slice means they are sufficient. The kit turns a non-empty one into a
	// refusal that names the policy.
	Problems(ctx context.Context, tier CredentialTier) ([]Problem, error)

	// Policy renders the permissions a tier needs, in whatever the vendor's
	// permission language is. The kit prints it and reads nothing in it.
	Policy(tier CredentialTier) (string, error)
}

// CredentialTier is how much power a command needs: standing up an account's
// bootstrap needs more than shipping a release into it.
type CredentialTier string

const (
	TierBootstrap CredentialTier = "bootstrap"
	TierDeploy    CredentialTier = "deploy"
)

// Identity is who the provider is acting as. Provider, Account and Principal are
// the fields the CLI branches on; everything else a vendor wants the user to see
// goes in Details as a label and a value, and the CLI prints it without knowing
// what it means.
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

// Problem is one missing permission, in the vendor's own vocabulary.
type Problem struct {
	Action string
	Reason string
}
