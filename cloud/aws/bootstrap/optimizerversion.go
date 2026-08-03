package bootstrap

// The image-optimizer artifact this provider build ships: which GitHub release
// asset it downloads into a customer's account, and the sha256 that asset must
// have. Hand-bumped, exactly like RequiredBootstrapVersion in version.go — the
// release workflow rewrites this file and the bump lands in a reviewable diff.
//
// The digest is deliberately NOT fetched from the same place as the zip. A
// digest published beside the artifact it describes proves nothing: whoever can
// replace one can replace the other. Pinning it in a reviewed source file is the
// whole of the fail-closed guarantee — bootstrap downloads the asset, hashes the
// bytes it actually received, and refuses to deploy anything that does not match
// (ensureOptimizerArtifact in optimizer.go).
//
// BOTH CONSTANTS ARE EMPTY PLACEHOLDERS AND MUST BE FILLED AT RELEASE.
// No release asset has been cut yet, so nothing can be verified and this build
// pins nothing. An unpinned build creates no optimizer function: bootstrap says
// so and skips it, the stack renders no Lambda, no Function URL output exists,
// the worker binds no image origin, and every valid /_next/image request stays
// the 502 it is today. That degradation is deliberate — a placeholder digest
// that let an unverified artifact through would be worse than no optimizer.
//
// To pin a release, in one commit:
//  1. `pnpm --filter @ocel/image-optimizer zip` and record
//     `sha256sum packages/image-optimizer/dist/image-optimizer.zip`.
//  2. Publish that zip as `optimizerAssetName` on a GitHub release tagged
//     `image-optimizer-v<version>` (see optimizerReleaseURL).
//  3. Set both constants below to that version and that digest.
const (
	// ImageOptimizerArtifactVersion is the released artifact's version, which is
	// also the release tag it is published under.
	ImageOptimizerArtifactVersion = "0.0.2"

	// ImageOptimizerArtifactSHA256 is the lowercase hex sha256 of that asset's
	// bytes. The build-zip script produces a reproducible archive (fixed
	// timestamps, sorted entries), so this digest is stable across machines.
	ImageOptimizerArtifactSHA256 = "cf9fbda3b06deee93898bf00e5f629b2889f456b562839bc16e7312c3277f383"
)
