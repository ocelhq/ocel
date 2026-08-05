package bootstrap

// The revalidator artifact this provider build ships: which GitHub release
// asset it downloads into a customer's account, and the sha256 that asset must
// have. Hand-bumped, exactly like RequiredBootstrapVersion in version.go and
// the publisher's pin in publisherversion.go.
//
// The digest is deliberately NOT fetched from the same place as the zip. A
// digest published beside the artifact it describes proves nothing: whoever can
// replace one can replace the other. Pinning it in a reviewed source file is the
// whole of the fail-closed guarantee — bootstrap downloads the asset, hashes the
// bytes it actually received, and refuses to deploy anything that does not match
// (ensureArtifact in artifact.go).
//
// BOTH CONSTANTS ARE EMPTY PLACEHOLDERS AND MUST BE FILLED AT RELEASE
// (ocelhq-wvag.27). No release asset has been cut yet, so nothing can be
// verified and this build pins nothing. An unpinned build creates no consumer
// at all: bootstrap says so and skips it, and the stack renders no Lambda, no
// event source mapping, no DLQ and no alarms.
//
// Nothing would then drain the revalidation queue — which is why the edge is
// told the queue URL only when this pin is present (ocelhq-wvag.24). Without
// it the edge never enqueues and every admitted refresh renders through
// originBlocking exactly as it does today: slower convergence under load, never
// a wrong serve and never a suppressed refresh. That degradation is deliberate
// — a placeholder digest that let an unverified artifact through would be worse
// than no consumer.
//
// To pin a new release, in one commit:
//  1. `pnpm --filter @ocel/revalidator zip` and record
//     `sha256sum packages/revalidator/dist/revalidator.zip`.
//  2. Publish that zip as `revalidatorAssetName` on a GitHub release tagged
//     `revalidator-v<version>` (see revalidatorReleaseURL).
//  3. Set both constants below to that version and that digest.
const (
	// RevalidatorArtifactVersion is the released artifact's version, which is
	// also the release tag it is published under.
	RevalidatorArtifactVersion = ""

	// RevalidatorArtifactSHA256 is the lowercase hex sha256 of that asset's
	// bytes. The build script produces a reproducible archive (fixed timestamps,
	// sorted entries), so this digest is stable across machines.
	RevalidatorArtifactSHA256 = ""
)
