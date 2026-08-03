# Next.js Image Optimization Pipeline — Design of Record

Status: agreed, in implementation. This document is the spec every PR in the stack is
built and reviewed against. If the code and this document disagree, one of them is a bug.

## Problem

`/_next/image` requests 404 today. Nothing in the repo handles them: `resolveRoutes`
matches nothing, so `serve()` falls through to `serveStaticAsset`, which misses colo cache,
misses R2, and returns the build's `404.html`. There is no image config in the routing
manifest, no image route in the worker, and no optimizer origin.

## Target flow

```
GET /_next/image?url=&w=&q=   (Accept: image/avif,image/webp,...)
  -> worker: validate against manifest image config      -> 400 on any failure
  -> worker: colo cache (caches.default) lookup          -> HIT / STALE(serve+revalidate)
  -> worker: R2 durable tier lookup                      -> HIT (mirror into colo)
  -> worker: SigV4 POST to shared optimizer Function URL
       -> origin: load image config from S3, verify hash
       -> origin: re-validate everything independently
       -> origin: fetch source (S3 for local, SSRF-safe fetch for remote)
       -> origin: transform with sharp
  -> worker: store in colo + R2, serve
```

## Behavioral contract

Platform semantics where Vercel documents them (cache key, cache scope, TTL derivation).
Next.js self-hosted semantics everywhere Vercel is silent (validation, error messages,
format negotiation, response headers).

Reference implementation to conform to: `packages/next/src/server/image-optimizer.ts`
at the Next version in `examples/next-test` (16.2.10). Ocel targets Next 16 only.

### Deliberate divergences from Next

Each divergence gets a fixture annotated as a divergence, so the conformance suite
asserts the difference rather than hiding it.

1. **Redirects are re-validated against the pattern allowlist on every hop.** Next
   explicitly does not ("for your convenience, these redirects do not need to satisfy
   remotePatterns"). On a shared multi-tenant optimizer, an open redirect on any tenant's
   allowlisted CDN would become a fetch primitive against our origin's network position.
2. **Connect-time IP validation.** Next resolves, validates, then calls `fetch(hostname)`,
   which re-resolves — a DNS-rebinding TOCTOU. We validate inside the lookup that the
   socket actually uses, so there is no window.
3. **`Accept-Encoding: identity`** on upstream fetches. Removes the decompression-bomb
   class by construction and makes `Content-Length` meaningful as a pre-check.
4. **`VIPS_BLOCK_UNTRUSTED` plus an explicit sharp loader allowlist.** Next applies no
   `sharp.block()` allowlist at all and never sets this env var. See CVE-2026-66066
   (CVSS 9.5, arbitrary file content disclosure via unfuzzed libvips operations).
5. **Cache scope is project-wide and deploy-surviving** (Vercel's model), not Next's
   per-build disk cache.

## Cache key

```
sha256([
  KEY_VERSION,          // bump to invalidate everything
  slug,                 // project
  sourceIdentity,       // local: content hash;  remote: absolute normalized URL
  width,
  quality,
  accept,               // normalized
  configHash,           // sha256 of the compiled image config
])
```

`configHash` is mandatory: without it, tightening `remotePatterns` could be bypassed by
entries admitted under the old config.

`sourceIdentity` for local images is the build-time content hash from the manifest's
asset hash map. Identical bytes across deploys produce the same key (cache survives
redeploy); changed bytes produce a new key (no staleness). Paths absent from the map
fall back to `buildId + path`, which is correct but deployment-scoped.

## TTL

`ttl = max(minimumCacheTTL, upstreamMaxAge)` where `upstreamMaxAge` is parsed from the
upstream `Cache-Control`, reading `s-maxage` first and falling back to `max-age`.
`Expires` is never consulted (Next does not consult it either).

On optimization failure fallback, `ttl = minimumCacheTTL` — upstream Cache-Control is
ignored on that path, matching Next.

Static-import images (`/_next/static/media`, `/_next/static/immutable/media`) get
`public, max-age=315360000, immutable`. Everything else gets
`public, max-age=<ttl>, must-revalidate`.

## Response headers

Emission order and values match Next:

```
Vary: Accept                     (unconditional)
Cache-Control: <as above>
ETag: <sha256 of output, base64url>   (upstream etag for passthrough responses)
Content-Type: <negotiated>
Content-Disposition: <images.contentDispositionType>, sanitized filename
Content-Security-Policy: <images.contentSecurityPolicy>
X-Nextjs-Cache: MISS | STALE | HIT
x-ocel-cache: HIT | MISS | STALE | BYPASS
```

`CacheStatus` in `workers/nextjs/src/cache.ts` gains `STALE`.

## Format negotiation

`getSupportedMimeType(formats, accept)`: resolve the best media type from `Accept`
against the configured `formats` array (array order is preference order), then apply
Next's literal guard — if `!accept.includes(resolved)`, the result is `''`.

**This guard means wildcard-only Accept headers (`*/*`, `image/*`) get no format
negotiation at all.** Any reimplementation that misses this diverges immediately. It is
undocumented upstream; it is asserted by fixture.

Output content type selection:
```
mimeType                                        if negotiation produced one
upstreamType                                    if it has a known extension and is not webp/avif
image/jpeg                                      otherwise
```

## Passthrough rules, in order, before format selection

1. Sniff content type from magic bytes on the **first 1024 bytes only**. Null, not
   `image/*`, or containing `,` -> 400 "The requested resource isn't a valid image."
   Never fall back to the upstream `Content-Type` header (CVE-2025-55173).
   The non-`image/*` rejection runs **before** any bypass branch — the ordering was the
   actual exploitable defect in that CVE.
2. `image/svg*` without `dangerouslyAllowSVG` -> 400 `"url" parameter is valid but image
   type is not allowed`.
3. `ANIMATABLE_TYPES` (webp, png, gif) that are animated -> returned unmodified.
4. `BYPASS_TYPES` (svg, x-icon, x-icns, bmp, jxl, heic) -> returned unmodified.
5. Otherwise transform. AVIF quality is rescaled `max(round(q * 50/80), 1)` with
   `effort: 3`. WebP/PNG use quality directly; JPEG uses `{quality, mozjpeg: true}`.
   Resize is `.rotate()` then `resize(width, undefined, {withoutEnlargement: true})`.

## Failure behavior

- sharp throws and an upstream content type exists -> serve the original bytes,
  `ttl = minimumCacheTTL`, with a diagnostic header marking it as passthrough. Never silent.
- sharp throws and no upstream content type -> 400.
- Origin unreachable or 5xx -> 502, **not cached**. A stale colo entry, if present, is served.

---

# The stack

Six PRs, each building on the previous, first based on `main`. Every PR must build and
pass its own tests standalone.

## PR 1 — adapter: image config + asset hashes into the routing manifest

Branch: `image-opt-manifest-config`

`packages/next-runtime/src/next-adapter.mts` currently reads only `config.basePath` and
`config.i18n` from the adapter's `config` object. Add an `images` section to the emitted
routing manifest.

Source of truth is the **versioned deployment adapter API** (`config.images`, typed
`Required<ImageConfigComplete>`), not `.next/images-manifest.json`. Captured example:
`examples/next-test/args.json` under `args.config.images`.

Compile globs at build time using Next's own vendored picomatch —
`next/dist/compiled/picomatch` — so semantics are identical by construction:

```
hostname: makeRe(p.hostname).source              // note: no dot:true
pathname: makeRe(p.pathname ?? "**", {dot:true}).source
```

Requirements:
- Normalize `URL` instances in `remotePatterns` to `RemotePattern` objects — the type is
  `Array<URL | RemotePattern>` and `URL` does not survive JSON serialization.
- Emit an asset content-hash map: `path -> sha256`, covering **both** `outputs.staticFiles`
  and the `public/` walk. `public/` is the more important of the two — it is where
  `<Image src="/logo.png" />` resolves — and omitting it silently defeats the
  deploy-surviving cache key for the most common case. Hash while streaming the copy;
  do not buffer whole files (a fully-buffered read imposes a ~2 GiB cap via
  `ERR_FS_FILE_TOO_LARGE` and loses the kernel reflink path).
- Emit `configHash` = sha256 over **the exact serialized bytes** of the image config
  artifact, so the origin can hash the file it downloaded without reimplementing our
  canonicalization.
- If `images.loader !== "default"` or `images.unoptimized === true`, **warn and omit the
  `images` section**. Do not fail the build. In both configurations `next/image` emits the
  original `src` and never generates a `/_next/image` URL, so there is nothing to serve —
  these are valid, common setups (external CDN, static-export-shaped apps) and a build
  failure would punish a correct configuration.
- The compiled hostname regex must reject `example.com.evil.com` for a `*.example.com`
  pattern. `makeRe` already emits `^(?:…)$`, which achieves this; do not add further
  wrapping. A differential harness against Next's real `matchRemotePattern` showed zero
  divergences across IPv6, punycode, IDN, userinfo, port and trailing-dot inputs, so any
  extra guard is unreachable in practice and would be an unregistered divergence.
  The `.hostname` contract is asserted in PR 3 instead, where the consumer lives.
- Write the compiled config as `image-config.json`, a sibling of `routing-manifest.json`
  in the adapter's output root. PR 2 uploads it to
  `image-config/<slug>/<app>/<buildID>.json` in the S3 asset bucket; the origin hashes
  those bytes and compares against `configHash`.
- Absent keys carry meaning and must survive the JSON round trip: unset `localPatterns`
  and unset `qualities` mean "allow", whereas `localPatterns: []` means deny-all. Omit
  the keys rather than emitting `undefined` or `[]`.

Tests: unit tests over the compiled output, including adversarial glob cases.

## PR 2 — deploy: mirror static assets and image config to S3

Branch: `image-opt-s3-mirror`

Today `cloud/aws/deploy/assets.go` uploads static assets to R2 only, at
`assets/<slug>/<app>/<buildID>/...`. S3 is used for function zips and fetch-cache.

Change: static assets go to **both** R2 and the account S3 asset bucket, under **identical
keys**.

The compiled image config goes to the S3 asset bucket **only**, at
`image-config/<slug>/<app>/<buildID>.json` — deliberately *not* under the `assets/`
prefix. `assets/<slug>/<app>/<buildID>/` is the app's public web root: the worker serves
any unmatched path from it (`key = ${assetPrefix}${url.pathname}`), so a config placed
there is served to the internet with `max-age=31536000, immutable`, disclosing every
allowed remote image hostname and the `dangerouslyAllowSVG` setting. It would also
key-collide exactly with a project's own `public/image-config.json`, producing a race whose
loser is either a silently replaced user asset or a config that fails the origin's hash
check and 502s every image for the life of the build.

There is no R2 copy of the config: the origin reads it from S3, and the worker takes the
compiled patterns and `configHash` from the routing manifest.

R2 remains the hot read tier; S3 becomes the source of truth for the asset plane and is
what the optimizer reads (so the optimizer never needs R2 credentials).

The ISR cache stays R2-only — the optimizer never reads it, and mirroring it would double
upload time and storage for no consumer.

Both uploaders already satisfy `ArtifactUploader { HeadObject; PutObject }`; reuse the
existing fan-out (`uploadConcurrency = 64`). A failure to either target fails the deploy.

Tests: deploy-path unit tests asserting both targets receive identical keys and bytes.

## PR 3 — worker: `/_next/image` route, validation, and the conformance oracle

Branch: `image-opt-edge-validation`

Add the route to `workers/nextjs/src/index.ts`. It must be handled **before** the
`serveStaticAsset` fallthrough. Origin is stubbed in this PR.

Validation must reproduce Next exactly, including error message text, in this order:

| Condition | Status | Message |
|---|---|---|
| `url` missing/empty | 400 | `"url" parameter is required` |
| `url` is an array | 400 | `"url" parameter cannot be an array` |
| `url.length > 3072` | 400 | `"url" parameter is too long` |
| `url.startsWith("//")` | 400 | `"url" parameter cannot be a protocol-relative URL (//)` |
| relative and decoded pathname matches `/\/_next\/image($\|\/)/` | 400 | `"url" parameter cannot be recursive` |
| relative and no `localPatterns` match | 400 | `"url" parameter is not allowed` |
| absolute and unparseable | 400 | `"url" parameter is invalid` |
| absolute and protocol not http/https | 400 | `"url" parameter is invalid` |
| absolute and no `domains`/`remotePatterns` match | 400 | `"url" parameter is not allowed` |
| `w` missing | 400 | `"w" parameter (width) is required` |
| `w` is an array | 400 | `"w" parameter (width) cannot be an array` |
| `w` fails `/^[0-9]+$/` or `<= 0` | 400 | `"w" parameter (width) must be an integer greater than 0` |
| `w` not in `deviceSizes ∪ imageSizes` | 400 | `"w" parameter (width) of ${w} is not allowed` |
| `q` missing | 400 | `"q" parameter (quality) is required` |
| `q` is an array | 400 | `"q" parameter (quality) cannot be an array` |
| `q` fails `/^[0-9]+$/` or outside 1..100 | 400 | `"q" parameter (quality) must be an integer between 1 and 100` |
| `qualities` set and `q` not in it | 400 | `"q" parameter (quality) of ${q} is not allowed` |

Critical details:
- The `//` check must run **before** the `startsWith("/")` relative/absolute branch.
  Getting this wrong is a full allowlist bypass (Next PR #65752, reported by GitBook).
- The recursion guard must parse and `decodeURIComponent` the pathname before matching.
  Prefix matching is bypassable via percent-encoding and assetPrefix (CVE-2024-47831).
- `/^[0-9]+$/` must run before `parseInt` — `parseInt("99.9") === 99`.
- Matching uses `new RegExp(compiledSource)` against the precompiled patterns from PR 1.
  No glob library in the worker.

Also in this PR: **the differential fixture generator**. A script runs the real Next server
in `examples/next-test` over a request matrix and records
`{status, body, contentType, cacheControl, vary, contentDisposition, csp}` into a
checked-in fixture file. Worker tests replay it.

The matrix must cover at minimum: every row above; each `Accept` variant including `*/*`
and `image/*` and absent; animated GIF; SVG with the flag on and off; ICO; a non-image
payload; a static-import media path; and the `localPatterns` search-string default.

Fixtures are regenerated deliberately, never blindly — a Next bump should surface as a
visible diff.

## PR 4 — worker: colo cache

Branch: `image-opt-colo-cache`

Wire the image route through the existing colo cache machinery in
`workers/nextjs/src/cache.ts` (`serveCached`, `storeInColo`, `refreshOnce`, `evaluate`).

- Cache key as specified above, including `configHash`.
- TTL derivation as specified above.
- Stale entries are served immediately while revalidating (`refreshOnce` already dedups
  in-flight refreshes per isolate).
- `x-ocel-cache` stamped on every image response; `CacheStatus` gains `STALE`.
- Entry format is **versioned** so PR 6 can add the R2 tier without a key change or flush.

Origin remains stubbed. Tests assert HIT/MISS/STALE transitions and that a `configHash`
change misses.

## PR 5 — origin: the shared image optimizer

Branch: `image-opt-origin-lambda`

### Compute

Node 22 + sharp, **arm64**, zip (~20 MB unzipped against a 250 MB cap), 1769 MB memory
(the first point with a full vCPU; libvips is genuinely multi-threaded so >1 vCPU is
usable). Response streaming, matching the existing Function URL pattern.

Own transform code — **not** `next/dist/server/image-optimizer`. Importing Next's
optimizer would pin this account-global function to a single Next version while it serves
every app in the substrate.

**sharp must be >= 0.35.0** (libvips >= 8.18.3). There is no 0.34.x backport for the four
2026 libvips CVEs. Do not copy OpenNext's pinned 0.32.6.

### Ownership and distribution

Account-global, created by bootstrap CloudFormation (`cloud/aws/bootstrap/`), which
creates no Lambda today. One function per AWS account, shared by preview and production.

The CLI holds a compiled-in artifact version and sha256, downloads the zip from a GitHub
release asset, verifies the digest **fail-closed**, and uploads it into the customer's own
bucket. CFN references it locally. No cross-account grants, no Region map. This is the
foundation the membrane-layer Region fix will later reuse.

### Trust model

The Worker holds **zero authority**. Edge sends
`{slug, app, buildId, url, w, q, accept, configHash}`.

The origin:
1. Loads the image config from `image-config/<slug>/<app>/<buildId>.json` in S3
   (same bucket and IAM grant it already needs for assets), memoized in-process by
   `configHash` across warm invocations.
2. Asserts `sha256(config) === configHash` — no downgrade.
3. Re-runs **full** validation against that config.
4. Never sees or forwards client headers. Internal fetches are unconditionally
   unauthenticated (this is what CVE-2025-57752 was: forwarded `Cookie` plus a cache key
   with no header component served one user's private image to everyone).
5. Guards path traversal on the derived S3 key.

### Security requirements — must pass review

**SSRF**
- Resolve, validate, and connect to the validated IP in one step, via undici
  `new Agent({connect: {lookup}})`. Do not validate-then-refetch-by-hostname.
- Default-deny IP policy: `ipaddr.parse(ip)`, unwrap IPv4-mapped IPv6, then
  `range() !== "unicast"`. Do not hand-roll a CIDR blocklist — the `ip` npm package has
  two CVEs for exactly that (CVE-2023-42282, CVE-2024-29415). This covers RFC1918,
  loopback, link-local incl. IMDS 169.254.169.254, CGNAT 100.64/10, 0.0.0.0/8, TEST-NETs,
  multicast, reserved, ULA fc00::/7, fe80::/10, NAT64, 6to4, Teredo.
- Normalize through `new URL()` before any IP check — this is what kills `0x7f000001`,
  `2130706433`, `0177.0.0.1`.
- Handle the `all` contract correctly in the custom lookup. Node 20+ `autoSelectFamily`
  defaults true and forces `all: true`; filter the whole list and return the whole
  filtered list. This is the most commonly botched part of the pattern.
- Follow redirects manually with `maxRedirections: 0`, cap at 3 hops, re-validate **both**
  the pattern allowlist and the IP on every hop, and `.dump()` every intermediate body.

**Resource limits**
- Count bytes incrementally as they stream, aborting on breach. Never `arrayBuffer()`.
  Apply to local S3 reads too — Next capped external first and earned a second CVE for the
  local path (CVE-2026-44577).
- Send `Accept-Encoding: identity`.
- Independent wall-clock timeout via `AbortSignal.timeout` — a byte cap does not fire
  against a slowloris origin.
- `limitInputPixels` at or below the 268402689 default; never 0/false.
- `sharp(...).timeout({seconds})` — the default is **no timeout**, and a small SVG with a
  large `feMorphology` radius hangs a worker forever.
- Never `unlimited: true`, never `failOn: "none"`, never `sequentialRead: false`, and
  never let `density` come from the request.

**sharp/libvips**
- Set `VIPS_BLOCK_UNTRUSTED` before the first sharp import.
- Explicit loader allowlist:
  ```js
  sharp.block({operation: ["VipsForeignLoad"]})
  sharp.unblock({operation: ["VipsForeignLoadJpegBuffer", "VipsForeignLoadPngBuffer",
                             "VipsForeignLoadWebpBuffer"]})
  ```
  The `*Buffer` suffix also structurally blocks the file-input-only SVG traversal
  (CVE-2023-38633).
- `sharp.cache(false)` — for unique untrusted inputs the hit rate is ~0 and it is pure
  attacker-influenced RSS plus retained fds.
- Pin both concurrency layers: `sharp.concurrency(n)` and `UV_THREADPOOL_SIZE`. Peak
  memory scales with their product.
- Generic client-facing error messages so the endpoint is not an SSRF oracle. Detail goes
  to logs only.

Next's own `packages/next/src/server/is-private-ip.test.ts` is a ready-made adversarial
corpus — lift it into this PR's tests.

## PR 6 — worker: R2 durable tier

Branch: `image-opt-r2-tier`

Read path becomes colo -> R2 -> origin. Write path: on an origin hit,
`waitUntil(colo.put + R2.put)`; on an R2 hit, `waitUntil(colo.put)` and serve.

Writes use SigV4 via `aws4fetch` and the existing edge credentials — the same mechanism
the worker already uses for signed origin calls. The R2 token is minted with the
"Workers R2 Storage Bucket Item Write" permission group, so no new credential or binding
is required. Verify this before building; if writes are not actually permitted, stop and
report rather than widening the token silently.

Layout:
```
assets/<slug>/<app>/<buildId>/**    static assets      (per build)
<env>/<slug>/<app>/<buildId>/**     ISR cache          (per build)
images/<slug>/<cacheKey>            transformed images (NEW, deploy-surviving)
```

Images deliberately sit outside any `buildId` prefix — the whole point of the content-hash
key is that entries outlive a single build. Preview and production already use separate R2
buckets, so environments are naturally isolated.

Writes are fire-and-forget: a failed write costs a future cache miss and nothing else.

---

## Notes for implementers

- The working tree has pre-existing untracked files unrelated to this work. **Stage only
  the paths you intend to commit** — never `git add -A` or `git add .`.
- `proto/` is generated; never hand-edit generated output.
- The SDK dogfoods itself; tests inject resource env vars directly so they run standalone.
- Do not add an AI or agent name as a commit co-author.
