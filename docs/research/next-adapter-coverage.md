# What a beefy Next example must exercise

Research for [#833](https://github.com/ocelhq/ocel/issues/833), part of the test-suite map
[#830](https://github.com/ocelhq/ocel/issues/830). Throwaway branch — never merged.

**Question.** Inventory the Next behaviours a deployment adapter is judged on, so the Next-extras
contract ticket picks from a candidate list instead of inventing one.

## Sources

Primary, all read directly.

- Next.js checkout at `~/Dev/next.js` at **v16.2.10** (`git log -1` → `9dadfd693c v16.2.10`;
  `package.json` is the workspace root and carries no version).
- `docs/01-app/03-api-reference/07-adapters/*` — adapter API, testing adapters, implementing PPR in
  an adapter, output types, invoking entrypoints, routing information.
- `docs/01-app/02-guides/{self-hosting,cdn-caching,deploying-to-platforms}.mdx`,
  `docs/01-app/03-api-reference/config/next-config-js/{adapterPath,cacheComponents,cacheHandlers,incrementalCacheHandlerPath,output}.mdx`.
- `packages/next/src/build/adapter/build-complete.ts`, `packages/next/src/lib/constants.ts`,
  `packages/next/src/client/components/app-router-headers.ts`.
- `test/lib/next-modes/next-deploy.ts`, `test/lib/e2e-utils/index.ts`, `test/e2e/**`,
  `test/production/adapter-config/**`.
- `test/deploy-tests-manifest.json` (Vercel's baseline) and `test/ocel-deploy-tests-manifest.json`
  (ours, copied there by CI from `scripts/e2e-next/baseline-manifest.json`).
- This repo: `scripts/e2e-next/`, `examples/next-cache-lab/`, `frameworks/next/`, `platform/aws/`,
  `platform/edge/cloudflare/`.

## How Next actually judges an adapter

There is no `test/deploy` directory. Deploy-mode conformance is `test/e2e/**` re-run with
`NEXT_TEST_MODE=deploy` against a live URL. Three facts from
`test/lib/next-modes/next-deploy.ts` and `test/lib/e2e-utils/index.ts` shape everything below.

1. **Only `test/e2e/**` runs in deploy mode.** `e2e-utils/index.ts:113-117` hard-pins
   `test/production/**` and `test/development/**` to `start`/`dev`. Every build-artifact assertion —
   the whole `test/production/adapter-config` suite — is therefore invisible to a deployed adapter.
2. **Deploy mode has no filesystem.** `next-deploy.ts:527-544` makes `patchFile`, `readFile`,
   `deleteFile` and `renameFile` throw. So "observable over plain HTTP" is not our invention: it is
   the exact boundary Next's own conformance suite operates within, plus a log-fetching hook.
3. **A third-party adapter plugs in through scripts**, not the Vercel CLI:
   `NEXT_TEST_DEPLOY_SCRIPT_PATH`, `NEXT_TEST_DEPLOY_LOGS_SCRIPT_PATH`,
   `NEXT_TEST_CLEANUP_SCRIPT_PATH` (`next-deploy.ts:46-157`). That is what `scripts/e2e-next` is.

### The consequence that decides this ticket

A large share of the sharpest assertions in `test/e2e` are wrapped `if (!isNextDeploy)` and are
**silently skipped** the moment the target is a real deployment. The compat harness is not a
superset of what we need. Concretely, in v16.2.10:

- `test/e2e/rewrites-redirects/rewrites-redirects.test.ts` **skips itself entirely** under
  `isNextDeploy` ("TODO: investigate test failures on deploy"). Config-level redirect and rewrite
  precedence, exact `Location`, exact `308`, is verified against **no** deployed target, ours or
  Vercel's.
- `test/e2e/app-dir/next-image/next-image.test.ts` gates its exact
  `/_next/image?url=…&w=…&q=…` query-shape assertions off on deploy; only `status` and
  `content-type` survive.
- `ppr-full.test.ts`'s cache-control / `x-nextjs-cache` table is `describe.skip` — dead code that
  documents intent but asserts nothing. `adapter-config-cache-components.test.ts` is likewise
  `describe.skip` (TODO NAR-423).
- `app-static.test.ts:139` skips the tag-budget warning on deploy because it reads
  `next.cliOutput`. Byte-equality cache checks are `if (isNextStart)`-only.
- Whole files carry `skipDeployment: true` (`on-request-error/isr`,
  `unstable-cache-foreground-revalidate`) and never run against any adapter at all.

**So the selection rule for the beefy example is: what does the compat harness skip, or catch only
after an hour of real deploys, that one app can prove in one HTTP round-trip?** Not "what does Next
test".

A few tests run in the *opposite* direction — deploy-only, written specifically to catch adapter
bugs. `app-static.test.ts:151` ("new tags have been specified on subsequent fetch") is
`if (isNextDeploy)`-only, and exists to catch an adapter serving stale content out of a per-instance
memory cache after tags change. `segment-cache/deployment-skew` asserts unconditionally that RSC
responses carry `content-type: text/x-component` and a truthy `x-nextjs-deployment-id`. Those two
are worth mirroring in our own harness precisely because they are the ones Next wrote for us.

## The header registry an adapter is graded against

From `packages/next/src/lib/constants.ts` and `client/components/app-router-headers.ts`. These are
the black-box surface; a contract that names them is a contract a harness can assert.

| header | meaning |
| --- | --- |
| `rsc` / `text/x-component` | RSC request marker / response content type |
| `x-matched-path` | internal rewrite target |
| `x-prerender-revalidate` | on-demand revalidate bypass token |
| `x-nextjs-deployment-id` | deployment-skew routing |
| `x-next-cache-tags` | tags on a cached response (format `_N_T_/layout,_N_T_/page,…,customTag`) |
| `x-next-revalidated-tags` | tags just invalidated |
| `x-nextjs-postponed` | PPR shell postponed, needs resume |
| `x-nextjs-prerender`, `x-nextjs-stale-time` | prerender fallback headers |
| `x-nextjs-rewritten-path` / `-query` | rewrite bookkeeping for RSC segment fetches |
| `x-nextjs-cache: HIT/MISS/STALE` | cache tier |
| `x-middleware-rewrite` | middleware rewrite marker |
| `vary: rsc, next-router-state-tree, next-router-prefetch, next-router-segment-prefetch` | required on prerender fallbacks |

`test/production/adapter-config/adapter-config.test.ts:261-267` pins a prerender fallback's
`initialHeaders` to exactly `content-type`, that `vary`, `x-next-cache-tags`,
`x-nextjs-prerender: 1`, `x-nextjs-stale-time: 300`. Build-artifact only — but it tells us what the
served response is supposed to look like.

Cache-Control literals a host must reproduce (`self-hosting.mdx`, `cdn-caching.mdx`,
`use-cache.test.ts:590-605`, `prerender.test.ts:678-747`):

- hashed `_next/static`: `public, max-age=31536000, immutable`, not overridable
- ISR / `use cache`: `s-maxage=<revalidate>, stale-while-revalidate=<expire - revalidate>`
- dynamic and draft mode: `private, no-cache, no-store, max-age=0, must-revalidate`
- and on Vercel's deploy path the client-visible header collapses to
  `public, max-age=0, must-revalidate` while `s-maxage` stays CDN-only — an adapter has to pick a
  split and the harness has to know which.

## What we already have

### `scripts/e2e-next/baseline-manifest.json` — the measured gap list

Currently **50 suites, 98 failing cases**, plus 2 suites carrying only flakes. Vercel's own
`deploy-tests-manifest.json` carries 9 suites of failures and excludes 28 more outright — nobody
passes everything. This file is the authoritative, already-paid-for gap list; the beefy example
should not re-derive it. Clusters by count:

| cluster | cases | what it means |
| --- | --- | --- |
| `next-after-app-deploy` | 8 | `after()`-triggered revalidation broken from page, action, route handler *and* proxy, in both runtimes |
| `app-dir/worker` | 7 | web workers, WASM in workers, `NEXT_DEPLOYMENT_ID` inside one |
| `prerender.test.ts` (pages) | 6 | pages SSG: 405 on POST to a static page, on-demand revalidate with preview cookie, `api-docs`-prefixed paths |
| `next-image` | 5 | app-dir image rendering, SSR and browser |
| edge capability (`edge-can-use-wasm-files`, `edge-async-local-storage`, `edge-api-endpoints-can-receive-body`) | 7 | |
| middleware/proxy (`middleware-general`, `middleware-rewrites`, `proxy-request-with-middleware`) | 6 | env vars in proxy; rewrite with body+headers; proxied GET/POST with `content-length` |
| `dynamic-route-interpolation` | 4 | params that are themselves `[brackets]` |
| `not-found-with-pages-i18n` | 4 | app 404 must beat pages 404 under i18n |
| `invalid-static-asset-404-*` | 4 | a bad `_next/static` path must be a plain-text 404 |
| trailing slash (`trailingslash`, `app-routes-trailing-slash`) | 4 | 308 + `Location`, and revalidating both forms |
| rewrite×revalidate (`revalidate-path-with-rewrites`, `rewrite-with-search-params`, `server-actions-redirect-middleware-rewrite`, `action-forward-loop`) | 4 | |
| `non-ascii-cache-tags` | 3 | see below |
| `app-static` | 3 | `updateTag`/`revalidateTag` from a server action; `unstable_cache` in `pages/api` |
| `og-api` | 3 | `ImageResponse` in pages/api, app route, proxy |
| `resume-data-cache` | 2 | static and dynamic renders disagree on `use cache` and fetch cache |
| deployment skew | 2 | `x-nextjs-deployment-id` on RSC and data responses |
| `revalidate-reason` | 2 | `"on-demand"` vs `"stale"` |
| `actions-streaming`, `app-action-node-middleware` | 2 | an action returning a `ReadableStream` must not buffer |
| rest | ~20 | basePath, prerender-encoding, partial-fallback-shell-upgrade, `use cache` in route handlers, esm/swc/async-module packaging, twoslash |

`non-ascii-cache-tags` deserves singling out: it is a regression test for a real production
incident (vercel/next.js#93142). Non-ASCII bytes in a cache tag, echoed into `x-next-cache-tags`,
crash Node's `setHeader` (`validateHeaderValue` rejects bytes outside `\t\x20-\x7e`), and on a
deployed target the resulting 500 was masked by `stale-if-error` while the cache silently stopped
refreshing. Any adapter that echoes tags into headers must sanitise them. Fully HTTP-observable,
and three of our 98 failures.

Two failures are known *by construction*, declared in our own source:

- `frameworks/next/adapter/src/next-adapter.mts:152` warns `"revalidate is inert for edge-rendered
  route(s) … edge ISR is not supported yet"` and marks those prerenders inert.
- `frameworks/next/adapter/src/edge-node-entry.cjs:98` — `TODO(#419)`: an edge route waived onto the
  origin function has no fetch-cache RPC, so its cached fetches always miss and its writes are
  dropped.

### `scripts/e2e-next/smoke-app` — six routes

Deliberately tiny; it exists to give the assert scripts something to poke.

| route | feature |
| --- | --- |
| `/` | static app-router page |
| `/isr`, `/q`, `/hdr` | `export const revalidate = 5` plus a `Date.now()` token; `/hdr` also sets `metadata.openGraph` |
| `/golden` | `revalidate = 3` and a fixed marker body, for the prefetch-suppression golden |
| `POST /api/revalidate-tag?tag=` | `revalidateTag(tag, "default")` |
| `proxy.ts` | rewrite `/mw/rewrite`→`/`, redirect `/mw/redirect`→`/`, direct 403 at `/mw/blocked`, `Set-Cookie` stamp on fall-through, matcher excluding `_next/static`/`_next/image`/`favicon.ico` |

Its assertions, all in `scripts/e2e-next/assert-*.mjs`, all run by hand today:

- **`assert-isr.mjs`** — the token must change after the window **and** the response carrying the
  new token must have `x-ocel-cache` in `{HIT, PRERENDER, STALE}`. The second half is the
  load-bearing one: it separates "re-rendered" from "actually revalidated a cache entry".
- **`assert-proxy.mjs`** — rewrite body equals `/`'s; redirect is 3xx to `/`; blocked is 403 with
  the expected body; `/` carries `Set-Cookie: ocel-proxy-seen=1`.
- **`assert-suppression-golden.mjs`** — a `purpose: prefetch` request must be byte-identical
  (status, body, non-volatile headers) to one without, for HTML and RSC, compared only while
  `x-nextjs-cache: STALE`.
- **`assert-tag-publisher.mjs`** — raises a tag over HTTP, then reads S3 snapshots and an SQS DLQ.
  Not black-box; AWS-shaped.
- **`assert-bytecode.mjs` / `assert-embed.mjs`** — CloudWatch markers. Not black-box, and not about
  Next behaviour.

### `examples/next-cache-lab` — 20 routes, zero assertions

`cacheComponents: true`, two custom `cacheLife` profiles (`editorial`, and `ticker` whose `expire`
is deliberately under five minutes so Next demotes it to a dynamic hole). Route cache:
`static` / `ppr` / `cached-layout` / `posts/[id]` / `search` / `dynamic`. Data cache: `profiles` /
`tags` / `component` / `interleaving` / `nested` / `remote` / `private` / `fetch-tags`. Handlers:
`POST /api/revalidate`, `GET /api/cached`, `GET /api/now`, plus `basic/endpoint` and
`basic/[tenantID]/endpoint` exercising all seven HTTP methods.

It is a *reading* app: every page prints a blue (in-cache) and an amber (request-time) stamp and a
human looks at them. Nothing is machine-asserted, the stamps were never meant to be scraped, and
its data comes from `jsonplaceholder.typicode.com` — a real network hop, so the app is not
hermetic. #830 already decides it is absorbed into the composite `next` example and deleted; this
is the ticket where "absorbed" has to mean "given stable, greppable markers and a local upstream".

### `frameworks/next` — what we implement

- **Adapter** (`adapter/src/next-adapter.mts`) — hooks `modifyConfig` and `onBuildComplete`.
  Installs a `cacheHandler` and patches `required-server-files.json` to point
  `cacheHandlers.{default,remote}` at Ocel handlers. Packs node routes into bundles; emits
  `routing-manifest.json` (dispatch table of `static`/`lambda`/`prerender`/`edge`), a `serve.json`
  declaring `needs` (`edge-middleware`, `edge-runtime`, `ppr-resume`, `edge-cache`, `streaming`),
  prerendered HTML/RSC/segment/postponed-state entries, fetch-cache entries, image config, and a
  Cloudflare worker bundle. Both routers, `basePath`, i18n passthrough, trailing slash, error-page
  substitution, node and edge middleware.
- **Router** (`router/src/index.mts`, ~1350 lines, host-neutral, driven through `RouteDeps` ports
  `fetch`, `originFetch`, `edge`, `prerender`, `imageOrigin`, `imageCache`, `assetStore`,
  `onRevalidated`) — `@next/routing`'s `resolveRoutes`, middleware invocation, rewrites/redirects/
  `has`, i18n locale resolution and redirect, trailing-slash and repeated-slash normalisation,
  dynamic params, `_next/data`, RSC/flight detection and `Vary`, segment prefetch, 404/500
  substitution, `x-matched-path` / `x-middleware-*`, `x-prerender-revalidate` bypass tokens, origin
  body budget with 413, transient retry.
- **Image** (`router/src/image.mts`) — width/quality/format negotiation from `Accept`, local and
  remote pattern allow-lists, static-import passthrough, recursive-URL rejection, absolute-URL
  length cap, cache-key derivation, and a 502 `"No image optimizer is provisioned"` fallback.
- **Cache** (`cache/src/`) — Next's tag budget (1024 B/tag, 1000 tags, 16 KB total), a stored-tag
  scheme, tag-clock merge, entry (de)serialisation for `FETCH` / `APP_ROUTE` / `APP_PAGE`, and a
  namespace binding env, project, app and release into keys.
- **HTTP cache** (`router/src/http-cache.mts`) — emits `x-ocel-cache`, `x-nextjs-cache` and
  `x-vercel-cache` (aliased, gated by `OCEL_E2E_VERCEL_CACHE_HEADER`).
- **Platform** — `platform/aws/provider/bootstrap/feature_isr.go` (ISR queue, revalidator,
  invalidator), `feature_imageopt.go` (one shared image transform), `platform/aws/functions/
  image-optimizer`, `platform/aws/membrane/src/next/isr-writer.mts`;
  `platform/edge/cloudflare/workers/entry/src/{cache,image,image-store}.ts` and
  `workers/isr-writer/src/{isr-snapshot,isr-deploy}.ts`. `cache.ts:29` holds
  `SUPPRESS_SELF_REVALIDATION = true`, the flag `assert-suppression-golden.mjs` names as its revert.

## The inventory

**Observable**: can a black-box client, given only the deployment URL, prove it? `yes` = one or two
requests. `partly` = provable, but the app must deliberately emit a marker, or the harness must
poll. `no` = needs filesystem, browser, cloud APIs or logs.

**Priority** is for the beefy example, not for the adapter. Something can be must-have behaviour
and still rank low here because the compat harness covers it better. It ranks *high* when the
compat harness skips it on deploy.

### Must

| # | behaviour | what a test asserts | observable | why must |
| --- | --- | --- | --- | --- |
| 1 | **Static prerender** | body byte-stable across requests; `cache-control: s-maxage=31536000` off-deploy | yes | the floor |
| 2 | **ISR by time** | body changes after the window, and the new body is served from a cache tier | partly — needs a render-time token and a cache-status header | our sharpest existing assertion; catches "re-renders but never writes an entry" |
| 3 | **On-demand `revalidateTag`** | after `POST`, the tagged page changes and an untagged control does not; `x-next-cache-tags` carries the tag | partly | 3 tag failures incl. from a server action; the whole point of a shared store. Include a **non-ASCII tag** — 3 more failures and a known production incident |
| 4 | **On-demand `revalidatePath`** | the named path changes, siblings do not | partly | `revalidate-path-with-rewrites` is red |
| 5 | **Dynamic SSR** | body changes every request; `private, no-cache, no-store, max-age=0, must-revalidate` | yes | the control case that makes every cache assertion mean something |
| 6 | **Route handlers** | GET/HEAD/OPTIONS/POST/PUT/DELETE/PATCH all reach the handler; request body arrives intact | yes | cheapest high-value probe; `edge-api-endpoints-can-receive-body` is red |
| 7 | **Proxy: rewrite / redirect / direct response / header injection** | rewrite serves the target's body at the source URL and sets `x-middleware-rewrite`; redirect is 3xx with the right `Location`; a direct response never reaches a route; injected request headers are visible downstream; POST body and `content-length` survive | yes | 6 failures across three middleware suites; `assert-proxy.mjs` already does the easy half |
| 8 | **Server actions** | a POST to the page's own URL mutates state and the next GET reflects it; an action returning a `ReadableStream` streams | partly — drive it as a form POST, or read back through a route handler sharing the store | actions are POSTs to the owning route, so every `matcher` mistake silently breaks them; 2 failures |
| 9 | **Streaming / Suspense** | shell arrives before deferred content; the response is chunked, not buffered | yes — read as a stream, assert a shell sentinel in an earlier chunk than a delayed one | ALB+Lambda buffering is the classic host failure |
| 10 | **PPR** | static shell served immediately, holes resume; `x-nextjs-postponed`, `x-nextjs-prerender`, `x-nextjs-rewritten-path` correct on segment-prefetch responses | partly | `cacheComponents` is the Next 16 default; **`ppr-full`'s header table is `describe.skip` and most postponed-header assertions are `!isNextDeploy`-gated**, so the compat harness barely checks this |
| 11 | **`use cache` + `cacheTag` + `cacheLife`** | an in-cache stamp freezes and only moves on revalidation; nested scopes inherit or override lifetime; `s-maxage=<revalidate>, stale-while-revalidate=<expire-revalidate>` | partly | this *is* the Next 16 caching model, and where `next-cache-lab` lands |
| 12 | **Static assets** | hashed `_next/static` carries `public, max-age=31536000, immutable`; `public/` files serve; a bogus `_next/static` path is a plain-text 404 | yes | 4 failures on the 404 case alone; the immutable header is not overridable |
| 13 | **not-found and error** | a missing route is 404 with the app's `not-found` and a `noindex` robots tag; a thrown error is 500 with `error.tsx`, not a stack | yes | trivially cheap; `global-not-found`, `error-handler-not-found-req-url` red |
| 14 | **Dynamic routes** | `[id]`, `[...slug]`, `[[...opt]]` bind params; `generateStaticParams` ids prerender while others render on demand; a URL-encoded segment (`/sticks%20%26%20stones`) round-trips without double-decoding | yes — echo params in the body | `dynamic-route-interpolation` (4) and `prerender-encoding` (1) are red |
| 15 | **RSC and prefetch** | an `rsc` request returns `text/x-component`, not HTML; `Vary` names the four router headers; a `purpose: prefetch` request has no side effect on the entry | yes | `cdn-caching.mdx` is explicit that stripping `rsc` breaks navigation; our golden covers the last half |
| 16 | **`next.config` redirects / rewrites / headers** | exact status (308 vs 307), exact `Location`, `has`/`missing` matching, and the order headers → redirects → proxy → beforeFiles → fs → afterFiles → dynamic → fallback | yes | **`rewrites-redirects.test.ts` self-skips on deploy**, so no deployed adapter is verified on this anywhere, ours or Vercel's. Highest "harness blind spot" score in the list |

### Should

| # | behaviour | what a test asserts | observable | note |
| --- | --- | --- | --- | --- |
| 17 | **`after()`** | work scheduled after the response completes still runs, including a revalidation | partly — read the side effect through a second route | **8/8 failures.** The most broken thing we have, and one route plus one readback proves it |
| 18 | **Fetch cache** | an upstream is hit once across N requests; `next.tags` and `next.revalidate` behave like route-level ones; static and dynamic renders agree | partly — needs a counting upstream | `resume-data-cache` is red, and its own test drives it purely through `rsc` / `next-router-prefetch` / `next-router-segment-prefetch` headers and body polling, so it is a ready-made pattern |
| 19 | **Image optimisation** | `/_next/image?url=&w=&q=` returns an image, negotiates AVIF/WebP from `Accept`, 400s on a disallowed remote host or a bad `w`/`q` | yes | 5 failures, and **the exact query-shape assertions are `!isNextDeploy`-gated** — the negative cases are cheaper and sharper than the positive one |
| 20 | **Draft mode** | `__prerender_bypass` cookie bypasses every cache, forces `private, no-cache, no-store`, and survives a 307 redirect | yes | one enable route, one disable route, one page that renders differently |
| 21 | **Edge vs node runtime** | both serve; `process.env.NEXT_RUNTIME` differs; edge gets `waitUntil` | yes — echo the runtime | 7 edge-capability failures, and **edge ISR is known-unsupported by us** |
| 22 | **Deployment id / skew** | `x-nextjs-deployment-id` truthy on RSC and data responses; `?dpl=` on assets | yes | 2 failures, asserted unconditionally by Next, and the journey's redeploy step is exactly when two releases coexist |
| 23 | **Trailing slash** | 308 with the right `Location` on the non-canonical form; revalidation works on both forms | yes | 4 failures — but the flag is global, see below |
| 24 | **Instrumentation** | `register()` runs once per instance at cold start, before the first request | partly — record it, expose it on a route | cheap; catches an adapter that never calls the hook. Next's own version asserts through logs, which we can do better |

### Could

| # | behaviour | why not higher |
| --- | --- | --- |
| 25 | **i18n** | App Router has no built-in i18n config — it is hand-rolled routing, so a route proves our routing, not Next's. The pages-router failures (`i18n-api-support`, `not-found-with-pages-i18n`) belong to the compat harness |
| 26 | **`basePath`** | global config, needs its own app. 2 failures |
| 27 | **`use cache: private` / `: remote`** | `private` is experimental and never server-stored, so little for a host to get wrong; `remote` only diverges once a real `cacheHandlers` backend is wired, which `next-cache-lab` notes it does not have |
| 28 | **Metadata, `ImageResponse`, OG routes** | 3 `og-api` failures, but the assertion is "is this a valid PNG" — a heavy dependency for the harness |
| 29 | **Web workers, WASM** | 7+ failures, but they are bundling concerns, browser-observable rather than HTTP-observable |
| 30 | **Pages Router** | 8+ failures across `prerender.test.ts` and friends. Real, but a composite App Router example is the wrong home; if it matters it wants a `hello-next`-shaped sibling |

### Not observable over HTTP — leave to the compat harness, a Go test, or nothing

Cache-entry layout on disk; `routes-manifest` / `prerender-manifest` / `functions-config-manifest` /
`required-server-files` shape and the whole `test/production/adapter-config` contract (every output's
`filePath`/`assets`/`wasmAssets` existing, static metadata files landing as `STATIC_FILE`, every app
page having both `.rsc` and non-`.rsc` outputs, edge outputs carrying
`NEXT_SERVER_ACTIONS_ENCRYPTION_KEY` and `edgeRuntime.handlerExport === 'handler'`, prerender
`initialHeaders` exactness, `preferredRegion` propagation); deterministic-build and NFT-trace
snapshots; S3 tag snapshots and SQS DLQ depth; CloudWatch bytecode markers; `next.cliOutput`
warnings such as the tag-budget one; cold-start counts; anything needing a browser.

Worth noting that Next's own deploy mode reaches *some* of this through the logs script hook
(`NEXT_TEST_DEPLOY_LOGS_SCRIPT_PATH`), which we already implement as `scripts/e2e-next/logs.mjs`.
If the journey harness ever wants a log-shaped assertion, that hook is the precedent — but it is a
target-specific escape hatch, not part of an HTTP contract, and should stay out of the extras.

## What the app must do to make these assertable

The recurring constraint, and the thing the contract ticket has to settle.

1. **Every cacheable route prints two stamps with stable ids** — one produced inside the cache
   scope, one at request time. `next-cache-lab` has the concept (blue/amber); it needs ids like
   `data-cache:tags:cached=<n>` and `:live=<n>` so a harness greps instead of a human squinting.
   This one convention makes items 1–4, 10, 11 and 18 assertable.
2. **A cache-status header on every response, with a named vocabulary.** We emit `x-ocel-cache`;
   the extras contract should pin its tiers (`HIT`/`MISS`/`STALE`/`PRERENDER`), because without it
   every cache assertion degrades to "the body changed", which cannot tell a revalidation from a
   plain re-render.
3. **A counting upstream inside the app.** One route handler that increments and reports a counter
   is the only honest way to assert fetch-cache and `use cache` hit behaviour, and it removes
   `next-cache-lab`'s dependency on `jsonplaceholder.typicode.com`.
4. **A side-effect readback route.** `after()`, server actions and on-demand revalidation all need
   "did it happen" answered by a second request. One `GET /probe/state` over a store the SDK already
   provides covers all three.
5. **A streaming sentinel, not a timing assumption** — a marker in the shell, a different one after
   an artificial delay, and an assertion on chunk order.
6. **A tag with a non-ASCII character**, because it costs one string and covers a class of bug that
   fails silently behind `stale-if-error`.

## Recommendation for the contract ticket

The **must** block is sixteen behaviours and, given the six conventions above, roughly twenty
routes — the size `next-cache-lab` already is, so the composite example does not grow beyond what
exists today. Item 16 (config redirects/rewrites/headers) is the single best candidate in the whole
list: it is cheap, fully HTTP-observable, and verified by **no** deployed-target suite anywhere,
including Vercel's own.

In **should**, item 17 (`after()`) has the best failure-to-cost ratio: eight recorded failures, one
route and one readback.

Items 23 (`trailingSlash`) and 26 (`basePath`) need a global config flag and so cannot sit in the
composite example without changing it for every other assertion. Either they stay the compat
harness's problem or they get a `hello-next`-shaped sibling; the contract ticket should say which
rather than leaving them implicitly in.

Everything in **could** should be named as explicitly out, with the compat harness cited as what
covers it, so the composite example stops accreting.
