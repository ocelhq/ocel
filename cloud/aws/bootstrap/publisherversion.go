package bootstrap

// The tag-publisher artifact this provider build ships: which GitHub release
// asset it downloads into a customer's account, and the sha256 that asset must
// have. Hand-bumped, exactly like RequiredBootstrapVersion in version.go and
// the optimizer's pin in optimizerversion.go.
//
// The digest is deliberately NOT fetched from the same place as the zip. A
// digest published beside the artifact it describes proves nothing: whoever can
// replace one can replace the other. Pinning it in a reviewed source file is the
// whole of the fail-closed guarantee — bootstrap downloads the asset, hashes the
// bytes it actually received, and refuses to deploy anything that does not match
// (ensureArtifact in artifact.go).
//
// BOTH CONSTANTS ARE EMPTY PLACEHOLDERS AND MUST BE FILLED AT RELEASE
// (ocelhq-wvag.14). No release asset has been cut yet, so nothing can be
// verified and this build pins nothing. An unpinned build creates no publisher:
// bootstrap says so and skips it, the stack renders no Lambda, no stream
// consumer, no DLQ and no alarm, and a build's tag-clock replica is published by
// whatever published it before — which, until ocelhq-wvag.6 removes it, is the
// Lambda tier's own publisher and the DynamoDB fallback behind it. That
// degradation is deliberate: a placeholder digest that let an unverified
// artifact through would be worse than no publisher.
//
// To pin a release, in one commit:
//  1. `pnpm --filter @ocel/tag-publisher zip` and record
//     `sha256sum packages/tag-publisher/dist/tag-publisher.zip`.
//  2. Publish that zip as `tagPublisherAssetName` on a GitHub release tagged
//     `tag-publisher-v<version>` (see tagPublisherReleaseURL).
//  3. Set both constants below to that version and that digest.
const (
	// TagPublisherArtifactVersion is the released artifact's version, which is
	// also the release tag it is published under.
	TagPublisherArtifactVersion = ""

	// TagPublisherArtifactSHA256 is the lowercase hex sha256 of that asset's
	// bytes. The build script produces a reproducible archive (fixed timestamps,
	// sorted entries), so this digest is stable across machines.
	TagPublisherArtifactSHA256 = ""
)
