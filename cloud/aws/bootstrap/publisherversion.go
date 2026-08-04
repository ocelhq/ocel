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
// Both constants are pinned (ocelhq-wvag.14, 2026-08-04). A build that left them
// empty creates no publisher at all: bootstrap says so and skips it, and the
// stack renders no Lambda, no stream consumer, no DLQ and no alarms.
//
// Nothing would then carry an origin-raised invalidation to a build's edge
// replica. The Lambda tier's own publisher was deleted when the durable tag
// write became the whole raise, so this consumer is the only thing that would
// have carried it; only invalidations raised at the edge itself reach the
// replica. What that costs is bounded: the origin reads the state table, which
// is the authoritative clock and is unaffected, so an unpinned build serves
// correct pages and a stale edge replica. That degradation is deliberate — a
// placeholder digest that let an unverified artifact through would be worse than
// no publisher.
//
// To pin a new release, in one commit:
//  1. `pnpm --filter @ocel/tag-publisher zip` and record
//     `sha256sum packages/tag-publisher/dist/tag-publisher.zip`.
//  2. Publish that zip as `tagPublisherAssetName` on a GitHub release tagged
//     `tag-publisher-v<version>` (see tagPublisherReleaseURL).
//  3. Set both constants below to that version and that digest.
const (
	// TagPublisherArtifactVersion is the released artifact's version, which is
	// also the release tag it is published under.
	TagPublisherArtifactVersion = "0.0.1"

	// TagPublisherArtifactSHA256 is the lowercase hex sha256 of that asset's
	// bytes. The build script produces a reproducible archive (fixed timestamps,
	// sorted entries), so this digest is stable across machines.
	TagPublisherArtifactSHA256 = "7b9350c95704369fa5f13ab16f434e1e23e3c19dc87a0d4dec8480f7607bdc5c"
)
