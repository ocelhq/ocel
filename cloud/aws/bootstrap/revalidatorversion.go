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
// BOTH CONSTANTS ARE EMPTY AND STAY SO UNTIL `revalidator-v0.0.1` IS PUBLISHED
// (ocelhq-wvag.27; the exact diff that fills them is below). No release asset
// has been cut yet, so this build pins nothing. An unpinned build creates no consumer
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
// THE ARTIFACT IS BUILT AND ITS DIGEST IS ESTABLISHED; ONLY THE RELEASE IS
// OUTSTANDING (ocelhq-wvag.27). `pnpm --filter @ocel/revalidator zip` was run
// three times from a clean `dist/` on 2026-08-05 and produced a byte-identical
// 5843-byte archive each time:
//
//	sha256 2f830a670b3fbc9f313018375cb2f1d88f6b5950e986373079d212548ca8a0dd
//
// Those bytes were fed through ensureRevalidatorArtifact against that digest:
// they upload to `ocel-revalidator/0.0.1-<digest>.zip`, and one flipped byte is
// refused with "revalidator artifact <url> has sha256 <got>, but this build
// requires <want>; refusing to deploy it" and zero PutObjects. The constants
// stay empty regardless, because pinning a digest whose release asset does not
// exist would make every bootstrap fail on a 404 download rather than skip the
// consumer — an unpinned build is the honest state until the asset is public.
//
// To pin it, in one commit, once `revalidator-v0.0.1` exists with
// `revalidatorAssetName` attached (see revalidatorReleaseURL):
//
//	RevalidatorArtifactVersion = "0.0.1"
//	RevalidatorArtifactSHA256 = "2f830a670b3fbc9f313018375cb2f1d88f6b5950e986373079d212548ca8a0dd"
//
// and re-verify by downloading the published asset and hashing it, rather than
// trusting the local build — that download is the only thing that proves the
// release carries the reviewed bytes. For any LATER release: rebuild, record
// `sha256sum packages/revalidator/dist/revalidator.zip`, publish, set both.
const (
	// RevalidatorArtifactVersion is the released artifact's version, which is
	// also the release tag it is published under.
	RevalidatorArtifactVersion = ""

	// RevalidatorArtifactSHA256 is the lowercase hex sha256 of that asset's
	// bytes. The build script produces a reproducible archive (fixed timestamps,
	// sorted entries), so this digest is stable across machines.
	RevalidatorArtifactSHA256 = ""
)
