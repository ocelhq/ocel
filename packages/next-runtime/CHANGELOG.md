# @ocel/next-runtime

## 0.1.0

### Minor Changes

- 5db9ac7: Carry the Next `images` config and static-asset content hashes in the routing manifest

  The adapter now compiles `config.images` into an edge-consumable shape — remote and local
  pattern globs precompiled to regex sources with Next's own vendored picomatch, so matching
  semantics are identical by construction — and emits it both inline in the routing manifest
  and as a sibling `image-config.json` artifact. A `configHash` over the artifact's exact
  bytes lets a consumer verify the config it loaded is the one the build produced.

  It also emits `assetHashes`, a served-path to sha256 map covering `public/` and
  `.next/static`, hashed while streaming the copy. This is what lets an optimized-image cache
  key survive a redeploy without going stale when the bytes change.

  Apps that opt out of image optimization (`images.unoptimized`, or a non-default `loader`)
  build as before, with a warning and no image config — `next/image` emits the original `src`
  in those configurations and never requests `/_next/image`.
