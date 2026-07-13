package bootstrap

import "fmt"

// RequiredBootstrapVersion is the version of the account-global bootstrap
// resources this provider build requires. It is a standalone, monotonic
// integer — decoupled from the provider's release version — and is bumped
// only when the bootstrapped resources (the CloudFormation stack shape or the
// imperative steps around it) change in a way that older/newer providers
// can't tolerate. The bootstrap CloudFormation stack records the version it
// deployed as its BootstrapVersion output; every invocation compares the two.
// Version 2 added the account-global DynamoDB sessions table. Version 3 added
// the account-global function-artifact S3 bucket.
const RequiredBootstrapVersion = 3

// Compatibility is the outcome of comparing the deployed bootstrap version
// against the version a provider requires.
type Compatibility int

const (
	// Compatible means the deployed bootstrap matches what the provider
	// requires; work may proceed.
	Compatible Compatibility = iota
	// NeedsBootstrap means the account has no bootstrap, or an older one than
	// the provider requires: the user must (re-)run `ocel bootstrap`.
	NeedsBootstrap
	// NeedsCLIUpgrade means the deployed bootstrap is newer than the provider
	// understands: the user must upgrade the Ocel CLI/provider.
	NeedsCLIUpgrade
)

// CheckCompat compares a deployed bootstrap version against required. present
// is false when the account has no bootstrap stack at all, which is always
// NeedsBootstrap regardless of deployed.
func CheckCompat(deployed int, present bool, required int) Compatibility {
	switch {
	case !present || deployed < required:
		return NeedsBootstrap
	case deployed > required:
		return NeedsCLIUpgrade
	default:
		return Compatible
	}
}

// Explain renders the actionable, direction-aware error for a non-compatible
// outcome, or nil when compatible.
func (c Compatibility) Explain() error {
	switch c {
	case NeedsBootstrap:
		return fmt.Errorf("this AWS account is not bootstrapped for the current Ocel provider. Run `ocel bootstrap` and try again")
	case NeedsCLIUpgrade:
		return fmt.Errorf("this AWS account was bootstrapped by a newer Ocel provider than this one. Upgrade the Ocel CLI and try again")
	default:
		return nil
	}
}
