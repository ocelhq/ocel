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
6. **A malformed `Accept` parameter does not fail the request at the edge.** `@hapi/accept`
   throws on a parameter with no value (`Accept: image/webp;q`), so Next answers 500 before
   it looks at the image. The edge needs the negotiated type only as a cache-key component —
   the origin negotiates the response for itself, and its own 500 is not cached — so it
   declines to negotiate (empty media type) and lets the request through rather than
   manufacturing an error the tier that owns the response would raise anyway. Fixture:
   `accept-malformed-parameter`.
7. **`images.dangerouslyAllowLocalIP` is ignored.** Under `next start` the flag disables a
   check on a server the app owns alone, so the blast radius is that app. Our optimizer is
   account-global and holds read access to *every* tenant's asset plane, so honouring one
   app's flag would open IMDS — and that cross-tenant position — to anyone who can reach the
   route. The IP policy is default-deny with no per-app exception. An app that sets the flag
   gets no error and no effect.
8. **`X-Content-Type-Options: nosniff` on every served image.** Next emits none. This route
   serves attacker-influenced bytes from the app's own origin under a content type the
   optimizer picked, which is precisely where content-type confusion pays off — a bypass
   type returned unmodified is returned under a type the sniffer inferred, not one the
   source declared. The header costs nothing and bounds the damage of any future sniffer
   defect. (Review found exactly such a defect: a wildcard sentinel made `image/x-icon`
   match almost any payload.)

## Cache key

```
sha256([
  KEY_VERSION,          // bump to invalidate everything
  slug,                 // project
  sourceIdentity,       // local: content hash;  remote: absolute normalized URL
  width,
  quality,
  mimeType,             // the NEGOTIATED type — '' | image/avif | image/webp
  configHash,           // sha256 of the compiled image config
])
```

The `Accept` header enters the key only through `getSupportedMimeType(formats, accept)`,
never raw. Output bytes depend on `Accept` solely through that negotiation, and `formats`
is already covered by `configHash`, so keying on the resolved type is lossless — and it
collapses the combinatorial spread of real browser `Accept` strings that all resolve to
the same format onto a single entry. Keying on the raw header would fragment the cache
into many entries holding identical bytes.

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

The worker never talks to the upstream image server, so the origin contract is: **the
optimizer's response carries the upstream's `Cache-Control` as its own.** That mirrors
Next's internals, where the optimizer and the cache are the same process. The worker
parses it to derive `ttl`, then replaces it with the browser-facing value below — the
upstream directive is never forwarded to the client.

On optimization failure fallback, `ttl = minimumCacheTTL` — upstream Cache-Control is
ignored on that path, matching Next.

Static-import images (`/_next/static/media`, `/_next/static/immutable/media`) get
`public, max-age=315360000, immutable`. Everything else gets
`public, max-age=<ttl>, must-revalidate`.

## Response headers

Emission order and values match Next:

```
Vary: Accept                     (served images only — see below)
Cache-Control: <as above>
ETag: <sha256 of output, base64url>   (upstream etag for passthrough responses)
Content-Type: <negotiated>
Content-Disposition: <images.contentDispositionType>, sanitized filename
Content-Security-Policy: <images.contentSecurityPolicy>
X-Nextjs-Cache: MISS | STALE | HIT
x-ocel-cache: HIT | MISS | STALE | PRERENDER | BYPASS
```

`PRERENDER` on this route names the durable R2 tier (PR 6) answering: the colo cache
missed, the stored object was served in the optimizer's place, and no optimization ran.

Error responses carry none of these **except `x-ocel-cache`**. As the fixtures record,
every 400 and every 500 Next returns from this route is a bare body with no `Vary`, no
`Content-Type` and no `Cache-Control`; only a served image carries the rest of the header
block above.

`x-ocel-cache` is the one exception because it is Ocel's own diagnostic, not Next's: there
is no conformance fixture for it to match, and an operator debugging a 400 would otherwise
get no tier signal at all. It is stamped on **every** response this route returns —
validation errors included — which also removes the indefensible asymmetry of a 502
carrying it while a 400 does not. (A passthrough body
Next's own server compressed also picks up `Vary: Accept, Accept-Encoding` — an artifact of
that server's compression middleware, not of the optimizer.)

`CacheStatus` in `platform/edge/cloudflare/workers/entry/src/cache.ts` gains `STALE`, and it is emitted by **both**
route classes, not just images. Until now a stale colo serve on a prerendered route
reported `HIT`, on the reasoning that the header names the tier that answered and
staleness only drives the background refresh. That made one header mean two different
things once images needed to report freshness. A stale **colo** serve is now `STALE` on a
prerendered route exactly as on an image.

The R2 tier is the one place freshness stays out of this header: a stale R2 serve still
reports a bare `PRERENDER`, with its freshness in `X-Nextjs-Cache`. `PRERENDER` names the
tier that answered, and there is no `STALE_PRERENDER` worth minting for it.
This is a deliberate behavior change to the prerender path, and its existing tests and the
comments asserting HIT-when-stale change with it.

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
5. Otherwise transform. AVIF quality is rescaled `max(q - 20, 1)` with `effort: 3` —
   Next 16.2.10's `image-optimizer.js:885`, verified against the installed package. (An
   earlier draft of this document said `max(round(q * 50/80), 1)`, which appears nowhere in
   Next 16 and would have put the default `q=75` at 47 against Next's 55.) WebP/PNG use quality directly; JPEG uses `{quality, mozjpeg: true}`.
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

`frameworks/next/adapter/src/next-adapter.mts` currently reads only `config.basePath` and
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

Add the route to `platform/edge/cloudflare/workers/entry/src/index.ts`. It must be handled **before middleware
runs**, which is where Next handles it (`handleNextImageRequest` inside
`normalizeAndAttachMetadata`, ahead of `handleCatchallMiddlewareRequest`) — and therefore
also before the `serveStaticAsset` fallthrough. A broad matcher such as
`['/((?!api).*)']` otherwise redirects every image to the app's login page on our edge
while `next start` serves it, and costs an edge invocation per image. Route matching
strips `basePath` and then tests `/_next/image`, as Next does, so an app under a
`basePath` serves both the prefixed and the bare path. Origin is stubbed in this PR.

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

The table groups the rows by parameter for readability; Next interleaves the last
few, and the fixtures pin the interleaving. Both `q` presence checks (missing,
array, non-integer) run **before** `w`'s value checks (`<= 0`, not in
`deviceSizes ∪ imageSizes`), so `?w=0` with no `q` is a quality error and
`?w=99.9` with no `q` is a width error.

Critical details:
- The `//` check must run **before** the `startsWith("/")` relative/absolute branch.
  Getting this wrong is a full allowlist bypass (Next PR #65752, reported by GitBook).
- The recursion guard must parse and `decodeURIComponent` the pathname before matching.
  Prefix matching is bypassable via percent-encoding and assetPrefix (CVE-2024-47831).
- `/^[0-9]+$/` must run before `parseInt` — `parseInt("99.9") === 99`.
- Matching uses `new RegExp(compiledSource)` against the precompiled patterns from PR 1.
  No glob library in the worker.
- A `url` that neither `decodeURIComponent` nor `new URL` can handle (`/%`, `%2F%25`,
  `/a%zz`, `/\[/x.png`) makes Next throw and answer **500** with a bare
  `Internal Server Error`. The edge reproduces that status with a controlled response:
  an uncaught throw here is a Cloudflare 1101 "Worker threw exception" page, reachable
  unauthenticated by anyone who can spell the route.

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
`platform/edge/cloudflare/workers/entry/src/cache.ts` (`serveCached`, `storeInColo`, `refreshOnce`, `evaluate`).

`serveCached` as written cannot carry an image response. Its storability gate
(`storagePolicy`) demands `s-maxage`, which an image response — `public, max-age=<ttl>,
must-revalidate` — does not have, so every image would be refused storage. And
`fromStorage` overwrites `cache-control` with `public, max-age=0, must-revalidate`, which
would destroy the very TTL/`immutable` header this spec requires reach the browser.

So the shared read path is **extracted**, not branched: an internal
lookup → `evaluate` → serve-or-refresh core takes a policy, and `serveCached` and a new
`serveCachedImage` become thin callers that each own their storability gate and their
served-response headers.

```
colo(request, target, deps, policy, origin)   // shared core
serveCached(...)      -> colo(..., prerenderPolicy, ...)
serveCachedImage(...) -> colo(..., imagePolicy, ...)

policy = { storable(response), forServe(response, status, stale) }
```

Neither route class carries the other's rules, and the sequence exists once.

- Cache key as specified above, including `configHash`.
- TTL derivation as specified above.
- Stale entries are served immediately while revalidating (`refreshOnce` already dedups
  in-flight refreshes per isolate).
- `x-ocel-cache` stamped on every image response; `CacheStatus` gains `STALE`.
- Entry format is **versioned** so PR 6 can add the R2 tier without a key change or flush.

Origin remains stubbed. Tests assert HIT/MISS/STALE transitions and that a `configHash`
change misses.

## PR 5 — origin: the shared image optimizer

Split in two. The optimizer is where the entire security checklist lives and needs no AWS
to review or test; the distribution plumbing needs a cut GitHub release and a real account
apply, both human-gated. Bundling them would have held complete, reviewed security work
behind two manual steps.

- **PR 5a — `image-opt-origin-lambda`.** The Lambda itself: transform, validation, trust
  model, the SSRF / resource-limit / sharp hardening below, and the adversarial test
  corpus. Pure code, tested standalone, no AWS. Nothing binds it yet, so the worker still
  answers 502 exactly as it does today.
- **PR 5b — `image-opt-origin-wiring`.** Bootstrap CFN creates the function; the CLI
  downloads, verifies and uploads the artifact; the worker binds it. Both human-gated
  steps land here.

Source lives in a new **`platform/aws/functions/image-optimizer`**, not in `platform/aws/functions/entrypoints`.
Its lifecycle is the reason: this artifact is account-global, versioned independently and
pinned by a digest compiled into the CLI, whereas `lambda-entrypoints` ships per-deploy
alongside an app build. A separate package also keeps sharp — a heavy native dependency —
out of the per-deploy graph.

The deployable zip is built by declaring `linux/arm64/glibc` in the package's pnpm
`supportedArchitectures` and cross-installing the native binary. No Docker, no emulation,
reproducible in CI. The repo already ships per-platform native packages
(`packages/native-lib/provider-aws-*`), so this is the established pattern here.

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

(PR 5b.) Created by bootstrap CloudFormation (`cloud/aws/bootstrap/`), which creates no
Lambda today.

**One optimizer per substrate, not one per account.** An earlier draft said one function
per account shared by preview and production; that predates the fact that bootstrap deploys
two stacks and each carries its *own* `AssetBucket` and its own edge IAM user. The optimizer
reads originals and configs from the asset bucket, so a single shared function would need
read access to both — breaking the preview/production isolation the split buckets exist to
provide. Each stack creates its own, reading only its own bucket, invoked only by its own
edge user. No cross-substrate grant anywhere.

The edge user's existing Lambda-invoke statement is conditioned on the `ocel:app` tag being
present, and the optimizer belongs to no app. It gets **its own explicit Allow on its own
function ARN** rather than a loosened condition or a fabricated `ocel:app` tag: the edge
gains exactly one new named callable target, and every app function stays governed by the
tag as before.

**Accepted, and stated rather than assumed: nothing binds the caller's identity to the
`slug` it names.** One edge user serves every app in a substrate, so IAM cannot separate
projects at this seam — any principal that can invoke the Function URL can read any of that
substrate's `image-config/<slug>/…` artifacts. The scope is one customer's own projects
inside their own AWS account, called by their own edge identity, and the artifact is a
compiled allowlist rather than a secret. Binding it would require a per-app signing secret
distributed to and rotated across every worker, which is not worth it for an in-account
boundary.

The CLI's artifact version and sha256 live in a **hand-bumped Go constants file beside
`bootstrap/version.go`**, mirroring `RequiredBootstrapVersion`. The release workflow updates
it, so a digest change shows up in a reviewable diff. Fetching the digest from the same
place as the artifact would make verifying one against the other prove nothing.

The worker learns the Function URL from a new `OCEL_IMAGE_OPTIMIZER_URL` env var, not from
the routing manifest: the manifest describes a build, and an account-global function is not
a property of any build. Bootstrap runs before any app deploy, so the URL exists by the
time a worker is deployed. Absent leaves `imageOrigin` unbound and every valid image
request a 502 — today's behavior exactly, which is what makes 5a safe to land alone.
The call is SigV4-signed with the existing edge credentials, like every other Function URL
forward.

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

**Amended at implementation.** The paragraph below specified SigV4 via `aws4fetch` and
the existing edge credentials; the verification it demanded found that mechanism does not
exist. The bucket is R2, and R2's S3 API authenticates only against keys R2 itself issued
— the access key id is the Cloudflare API token's id and the secret is the hex SHA-256 of
the token's value (`cloud/edge/cloudflare/r2.go`, `mintToken`). No AWS IAM credential
authenticates there, whatever it is signed for; the worker does carry an S3 signer over
`OCEL_EDGE_ACCESS_KEY_ID`/`OCEL_EDGE_SECRET_KEY` (`signing.ts`, `awsServiceFetch`), but it
signs for AWS's S3 and R2 would reject its keys as unknown. The pair that would work is
derived from the R2 token at bootstrap and shipped out as Offers to the AWS side; it is
never bound to this worker, and binding it would be precisely the new credential the
section rules out. What the worker does already hold is
the `OCEL_CACHE_STORE` binding the ISR tier reads through, which is read/write and needs
no signing at all. Writes therefore go through `.put()` on that binding: no new
credential, no new binding, no signing — the section's own constraint, met more strictly
than by the mechanism it named.

~~Writes use SigV4 via `aws4fetch` and the existing edge credentials — the same mechanism
the worker already uses for signed origin calls. The R2 token is minted with the
"Workers R2 Storage Bucket Item Write" permission group, so no new credential or binding
is required. Verify this before building; if writes are not actually permitted, stop and
report rather than widening the token silently.~~

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
