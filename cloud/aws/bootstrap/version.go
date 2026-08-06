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
// the account-global function-artifact S3 bucket. Version 4 added the
// account-global asset S3 bucket for prerender configs + fallbacks. Version 5
// replaced the single-purpose sessions table with a generic pk/sk state table
// shared by every Ocel state entity — a key-schema change, so CloudFormation
// replaces the table and in-flight upload sessions are dropped. Version 6
// widened the edge user's inline policy to write the fetch cache and tag
// records, and is also the bump that finally delivers the state table's
// tag-sync index — that index shipped without one, so an account bootstrapped
// before it never received it. Both are in-place updates, but neither reaches an
// account that is never told to re-run bootstrap, and an edge running under the
// narrower policy 403s every cache write. Version 7 added the variable store:
// its own table, separate from the state table, and one KMS key per env class.
// An account bootstrapped before it has nowhere to put a value, so it is told
// to re-run bootstrap rather than failing at the first write.
// Version 8 added the per-substrate image optimizer: a Lambda behind a Function
// URL, its read-only role on that substrate's own asset bucket, and the edge
// user's named Allow on that one function. An account bootstrapped before it has
// no optimizer, so it is told to re-run bootstrap — the same hard version gate
// every earlier bump uses, with the same one-command remedy, because an account
// that is never told anything is an account that silently never gets one.
//
// What is deliberately SOFT is the optimizer's *presence* within a version-8
// account. The version output is stamped from this constant unconditionally, not
// from whether the function was created, so a substrate that reached version 8
// without an optimizer — a provider build pinning no artifact yet — still passes
// the gate and deploys normally. It publishes no Function URL, the worker binds
// no image origin, and every valid /_next/image request answers 502 exactly as
// it did before an optimizer existed. Failing the deploy on a missing optimizer
// would hold an app's whole deployment hostage to one route.
//
// Version 9 turned on the state table's DynamoDB stream (NEW_IMAGE) and hung
// the account-global tag-snapshot publisher off it: a Lambda, its event source
// mapping, and the SQS queue failed batches land in. Turning a stream on is an
// in-place table update — no replacement, no
// data loss — but an account that never re-runs bootstrap has no stream at all,
// and after this every tag invalidation reaches the edge's replica only through
// that stream. So the gate is hard, like every bump before it: an out-of-date
// account is told to re-run `ocel bootstrap` rather than left invalidating
// nothing.
const RequiredBootstrapVersion = 9

// seedingBootstrapVersion is what the *first* of a first bootstrap's two settles
// stamps. That pass exists only to raise the buckets the artifacts are uploaded
// into, so it runs before either is obtained and deliberately carries neither
// account-global Lambda — and if an artifact is then refused, bootstrap errors
// out with that stack already created. Stamping the required version there would leave a
// virgin account passing CheckCompat with no optimizer at all: `ocel up` would
// succeed and every image would 502 with nothing pointing at the cause. Stamped
// one short of it, the same account is told exactly what it needs to hear — its
// bootstrap is out of date, re-run `ocel bootstrap` — which is also honest, since
// what the seeding pass raised is precisely a pre-optimizer substrate.
//
// Only the second settle stamps RequiredBootstrapVersion. What it does NOT
// require is that either function exist: a build pinning no artifact at all
// skips the seeding pass entirely, settles once, and stamps the required version
// with no function, which is the deliberate softness described above.
const seedingBootstrapVersion = RequiredBootstrapVersion - 1

// Compatibility is the outcome of comparing the deployed bootstrap version
// against the version a provider requires.
type Compatibility int

const (
	// Compatible means the deployed bootstrap matches what the provider
	// requires; work may proceed.
	Compatible Compatibility = iota
	// NeedsBootstrapInit means the account has no bootstrap stack at all: the
	// user must run `ocel bootstrap` to create one.
	NeedsBootstrapInit
	// NeedsBootstrapUpgrade means the account has a bootstrap, but an older one
	// than the provider requires: the user must re-run `ocel bootstrap` to
	// upgrade it. It is kept distinct from NeedsBootstrapInit because the two
	// share only a remedy — telling someone with a working account that they
	// never bootstrapped sends them hunting the wrong problem.
	NeedsBootstrapUpgrade
	// NeedsCLIUpgrade means the deployed bootstrap is newer than the provider
	// understands: the user must upgrade the Ocel CLI.
	NeedsCLIUpgrade
)

// CheckCompat compares a deployed bootstrap version against required. present
// is false when the account has no bootstrap stack at all.
func CheckCompat(deployed int, present bool, required int) Compatibility {
	switch {
	case !present:
		return NeedsBootstrapInit
	case deployed < required:
		return NeedsBootstrapUpgrade
	case deployed > required:
		return NeedsCLIUpgrade
	default:
		return Compatible
	}
}

// Explain renders the actionable, direction-aware error for a non-compatible
// outcome, or nil when compatible. deployed and required are the versions that
// produced the outcome; cmd is the bootstrap command for the substrate being
// acted on — `ocel bootstrap --preview` for a preview one. Each message is two
// lines, diagnosis then remedy, so the remedy survives a narrow terminal: the
// CLI indents whole lines, not wrapped continuations.
func (c Compatibility) Explain(deployed, required int, cmd string) error {
	switch c {
	case NeedsBootstrapInit:
		return fmt.Errorf("this AWS account has no Ocel bootstrap.\nRun `%s` to create it, then try again", cmd)
	case NeedsBootstrapUpgrade:
		// A stack raised before the BootstrapVersion output existed reads as
		// zero, which is not a version anybody deployed.
		if deployed == 0 {
			return fmt.Errorf("this AWS account's Ocel bootstrap predates version tracking; this provider requires version %d.\nRun `%s` to upgrade it, then try again", required, cmd)
		}
		return fmt.Errorf("this AWS account's Ocel bootstrap is out of date: the account is at version %d, this provider requires version %d.\nRun `%s` to upgrade it, then try again", deployed, required, cmd)
	case NeedsCLIUpgrade:
		return fmt.Errorf("this AWS account's Ocel bootstrap is newer than this provider understands: the account is at version %d, this provider supports up to version %d.\nUpgrade the Ocel CLI and try again", deployed, required)
	default:
		return nil
	}
}
