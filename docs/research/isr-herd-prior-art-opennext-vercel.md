# ISR thundering-herd prior art: OpenNext & Vercel

Research notes on how OpenNext (AWS + Cloudflare adapters) and Vercel prevent the
cache-revalidation stampede for Next.js ISR. All claims cite primary sources: the
opennextjs/opennextjs-aws and opennextjs/opennextjs-cloudflare repos, vercel/next.js
source, opennext.js.org docs, vercel.com/docs, and AWS docs. Anything not traceable to a
primary source is marked **UNVERIFIED**.

Source snapshots used for permalinks:

- `opennextjs/opennextjs-aws` @ `3342279` (main, 2026-08)
- `opennextjs/opennextjs-cloudflare` @ `da4b7fc` (main, 2026-08)
- `vercel/next.js` @ `ab09c1f` (canary, 2026-08)

---

## 1. OpenNext AWS (`opennextjs/opennextjs-aws`)

### 1.1 Big picture

The herd problem is split into two distinct cases, solved by different mechanisms:

- **STALE (past `revalidate`, still servable)**: stale is served immediately; the
  *revalidation* is decoupled into an SQS FIFO queue whose dedup + FIFO grouping
  guarantee at-most-one regeneration per path per dedup window.
- **HARD miss/expiry**: blocking render in the invoked Lambda. There is **no OpenNext
  lock/lease across Lambdas**; the only cross-request coalescing is CloudFront's own
  edge request collapsing (per-POP), plus Next.js's in-process batcher (limited value on
  Lambda, which handles one request per instance at a time).

### 1.2 The revalidation queue (SQS FIFO)

Docs ([opennext.js.org/aws/config/overrides/queue](https://opennext.js.org/aws/config/overrides/queue)):

> "The default implementation use an SQS queue. This has the main advantage of being
> able to control the concurrency of the revalidations as well as avoiding trigerring
> the revalidation multiple times for the same route."

> "Before sending the response to the client, OpenNext will check if the route is stale
> and if it is, it will call the queue override to revalidate the route."

The trigger lives in `revalidateIfRequired` — after the Next server responds, OpenNext
inspects the `x-nextjs-cache` response header and enqueues iff it is `STALE`
([`packages/open-next/src/core/routing/util.ts#L311-L360`](https://github.com/opennextjs/opennextjs-aws/blob/33422799811e281a0882f2be24264730ae68fff0/packages/open-next/src/core/routing/util.ts#L311-L360)):

```ts
if (headers[CommonHeaders.NEXT_CACHE] === "STALE") {
  ...
  await globalThis.queue.send({
    MessageBody: { host, url: revalidateUrl, eTag, lastModified },
    MessageDeduplicationId: hash(`${rawPath}-${lastModified}-${eTag}`),
    MessageGroupId: generateMessageGroupId(rawPath),
  });
}
```

**Deduplication (`MessageDeduplicationId`)** — md5 of `path + lastModified + etag`.
Every concurrent stale hit for the same cached generation of a path produces the *same*
dedup id, so SQS FIFO's content-based dedup collapses the herd of enqueues into one
delivered message. In-code comment
([util.ts#L336-L341](https://github.com/opennextjs/opennextjs-aws/blob/33422799811e281a0882f2be24264730ae68fff0/packages/open-next/src/core/routing/util.ts#L336-L341)):

> "We need to pass etag to the revalidation queue to try to bypass the default 5 min
> deduplication window. ... If you need to have a revalidation happen more frequently
> than 5 minutes, your page will need to have a different etag to bypass the
> deduplication window. If data has the same etag during these 5 min dedup window, it
> will be deduplicated and not revalidated."

The 5-minute window is SQS's own FIFO dedup interval
([AWS SQS docs, `MessageDeduplicationId`](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/using-messagededuplicationid-property.html)).
Once the revalidation completes, `lastModified`/etag change, so the next staleness
produces a fresh dedup id.

**Grouping (`MessageGroupId`)** — the path is hashed into one of
`MAX_REVALIDATE_CONCURRENCY` (default 10) shards, `revalidate-0..N`
([`packages/open-next/src/core/routing/queue.ts#L17-L31`](https://github.com/opennextjs/opennextjs-aws/blob/33422799811e281a0882f2be24264730ae68fff0/packages/open-next/src/core/routing/queue.ts#L17-L31)):

> "Since we're using a FIFO queue, every messageGroupId is treated sequentially. This
> could cause a backlog of messages in the queue if there is too much page to revalidate
> at once. To avoid this, we generate a random messageGroupId for each revalidation
> request. We can't just use a random string because we need to ensure that the same
> rawPath will always have the same messageGroupId."

So: same path → same shard → sequential processing (never two concurrent revalidations
of one path from the queue), while global revalidation concurrency is bounded by the
shard count. The v2 docs
([opennext.js.org/aws/v2/inner_workings/isr](https://opennext.js.org/aws/v2/inner_workings/isr))
say the group id "ensures that revalidation requests for the same route are processed
only once."

The SQS sender itself is a thin `SendMessageCommand` wrapper
([`overrides/queue/sqs.ts`](https://github.com/opennextjs/opennextjs-aws/blob/33422799811e281a0882f2be24264730ae68fff0/packages/open-next/src/overrides/queue/sqs.ts));
a `direct` override skips the queue for local dev (docs warn it "risks duplicate
processing"), and `dummy` disables ISR.

### 1.3 The revalidation function → server callback

Docs ([opennext.js.org/aws/inner_workings/components/revalidation](https://opennext.js.org/aws/inner_workings/components/revalidation)):

> "The revalidation backend is used to read the queue and revalidate the routes. It is
> used for ISR only. For every received message it will trigger a `HEAD` request with
> `x-prerender-revalidate` header to the host."

Implementation
([`packages/open-next/src/adapters/revalidate.ts#L30-L92`](https://github.com/opennextjs/opennextjs-aws/blob/33422799811e281a0882f2be24264730ae68fff0/packages/open-next/src/adapters/revalidate.ts#L30-L92)):
a Lambda consuming the SQS queue sends
`HEAD https://{host}{url}` with headers
`x-prerender-revalidate: <previewModeId>` and `x-isr: 1`. In-code comment:

> "HEAD request is used b/c it's not necessary to make a GET request and have CloudFront
> cache the request. ... 'previewModeId' is used to ensure the page is revalidated in a
> blocking way in lambda"

Success is checked as `x-nextjs-cache === "REVALIDATED"` and status in
`[200, 307, 308, 404]`; failed records are returned to SQS (partial batch failure) for
retry.

**How Next.js treats that request**: `x-prerender-revalidate` is Next's own on-demand
revalidation header (`PRERENDER_REVALIDATE_HEADER`). When its value matches the build's
`previewModeId`, Next sets `isOnDemandRevalidate = true`
([`packages/next/src/server/api-utils/index.ts#L78-L108`](https://github.com/vercel/next.js/blob/ab09c1f4b45d2ee316353ff4352d7efbbef396b2/packages/next/src/server/api-utils/index.ts#L78-L108),
[`packages/next/src/server/lib/incremental-cache/index.ts#L240-L246`](https://github.com/vercel/next.js/blob/ab09c1f4b45d2ee316353ff4352d7efbbef396b2/packages/next/src/server/lib/incremental-cache/index.ts#L240-L246)),
which makes the response cache skip the stale-serving early-resolve and do a **blocking**
regeneration (see §3), answering `x-nextjs-cache: REVALIDATED`.

### 1.4 Suppressing Next's own background revalidation

Crucial detail: the server function must *not* let Next.js fire its usual background
revalidation on a stale hit (Lambda freezes after the response; and many Lambdas would
each fire one). OpenNext fakes a prefetch — in-code comment in
[`packages/open-next/src/core/requestHandler.ts#L177-L187`](https://github.com/opennextjs/opennextjs-aws/blob/33422799811e281a0882f2be24264730ae68fff0/packages/open-next/src/core/requestHandler.ts#L177-L187):

> "WORKAROUND: We pass this header to the serverless function to mimic a prefetch
> request which will not trigger revalidation since we handle revalidation differently.
> There is 3 way we can handle revalidation:
> 1. We could just let the revalidation go as normal, but due to race conditions the
>    revalidation will be unreliable
> 2. We could alter the lastModified time of our cache to make next believe that the
>    cache is fresh, but this could cause issues with stale data since the cdn will
>    cache the stale data as if it was fresh
> 3. OUR CHOICE: We could pass a purpose prefetch header ... (This could potentially
>    break in the future if next changes the behavior of prefetch requests)"

(Next's response cache serves stale without revalidating when
`context.isPrefetch` — see §3.) So on a stale hit the Lambda serves stale, marks the
response `STALE`, and the only regeneration path is the deduped queue.

### 1.5 CacheHandler / staleness signalling / S3

OpenNext replaces Next's cache handler with an S3-backed incremental cache
([`packages/open-next/src/adapters/cache.ts`](https://github.com/opennextjs/opennextjs-aws/blob/33422799811e281a0882f2be24264730ae68fff0/packages/open-next/src/adapters/cache.ts)).
Next's `CacheHandler` interface has no explicit "stale" flag, so OpenNext manipulates
`lastModified` — doc comment in
[`packages/open-next/src/utils/cache.ts#L38-L65`](https://github.com/opennextjs/opennextjs-aws/blob/33422799811e281a0882f2be24264730ae68fff0/packages/open-next/src/utils/cache.ts#L38-L65):

> "Next.js has no explicit way for a cache handler to report an entry as stale, the only
> lever we have is the `lastModified` we hand back to the incremental cache. Up to Next
> 16.2 we return `1` (i.e. right after the epoch), which is enough for Next to compute
> `revalidateAfter` in the past and mark the entry as stale. Starting with Next 16.3 the
> incremental cache also compares `lastModified + expire` to now, and forces a blocking
> revalidation (`isStale === -1` ...) when that is in the past."

On a **hard miss** (no S3 object, or tag-cache says the entry was revalidated), the
handler returns `null`/expired, and Next does a blocking render in that Lambda. There is
**no in-flight request dedup across Lambdas** anywhere in `opennextjs-aws` — no lock,
lease, or coalescing in the incremental cache override. Mitigations that do exist:

- **CloudFront request collapsing** (per edge location, same cache key):
  [AWS CloudFront docs — "Simultaneous requests for the same object (request collapsing)"](https://docs.aws.amazon.com/AmazonCloudFront/latest/DeveloperGuide/RequestAndResponseBehaviorCustomOrigin.html#request-custom-traffic-spikes):
  > "if additional requests for the same object (with the same cache key) arrive at the
  > edge location before CloudFront receives the response to the first request —
  > CloudFront pauses before forwarding the additional requests to the origin. ...
  > CloudFront sends the response from the original request to all the requests that it
  > received while it was paused."
- **Next's in-process batcher** (§3) coalesces concurrent renders of one path *within
  one server process* — of limited value on Lambda since "one instance of your function
  handles one request at a time" ([AWS Lambda scaling docs](https://docs.aws.amazon.com/lambda/latest/dg/lambda-concurrency.html)).

### 1.6 Cache interception (routing layer)

Optionally, OpenNext's routing layer serves ISR/SSG responses straight from the
incremental cache without invoking the server function
([`packages/open-next/src/core/routing/cacheInterceptor.ts`](https://github.com/opennextjs/opennextjs-aws/blob/33422799811e281a0882f2be24264730ae68fff0/packages/open-next/src/core/routing/cacheInterceptor.ts)).
It computes staleness itself (`age > revalidate`, or tag-cache stale) and, when stale,
serves the cached body and enqueues the *same deduped* queue message
([#L93-L102](https://github.com/opennextjs/opennextjs-aws/blob/33422799811e281a0882f2be24264730ae68fff0/packages/open-next/src/core/routing/cacheInterceptor.ts#L93-L102)):
`MessageDeduplicationId: hash(`${path}-${lastModified}-${etag}`)`,
`MessageGroupId: generateMessageGroupId(path)`. Responses are emitted with
`cache-control: s-maxage=<ttl>, stale-while-revalidate=<1 month>` and
`x-opennext-cache: STALE|HIT` so CloudFront serves stale during the queue-driven
regeneration.

### 1.7 Tag cache (DynamoDB)

The DynamoDB table is **not** part of the herd mechanism; it exists for
`revalidateTag`/`revalidatePath`. It stores `tag`, `path`, `revalidatedAt` rows
([v2 ISR docs](https://opennext.js.org/aws/v2/inner_workings/isr): "We use a separate
table to store the tags for each route"); implementations in
[`overrides/tagCache/dynamodb.ts`](https://github.com/opennextjs/opennextjs-aws/blob/33422799811e281a0882f2be24264730ae68fff0/packages/open-next/src/overrides/tagCache/dynamodb.ts).
On reads the cache handler asks `getLastModified(path, lastModified)` /
`isStale(tags, lastModified)`; if any tag was revalidated after the entry's
`lastModified`, the entry is treated as expired (pre-Next 16) or stale-servable
(Next ≥ 16, which added SWR semantics for `revalidateTag` — see
[`utils/cache.ts#L18-L36`](https://github.com/opennextjs/opennextjs-aws/blob/33422799811e281a0882f2be24264730ae68fff0/packages/open-next/src/utils/cache.ts#L18-L36):
"SWR for revalidateTag has been implemented starting from Next.js 16").

---

## 2. OpenNext Cloudflare (`opennextjs/opennextjs-cloudflare`)

Different substrate, same shape: the docs
([opennext.js.org/cloudflare/caching](https://opennext.js.org/cloudflare/caching))
describe three components — an Incremental Cache (R2/KV/static assets), "A Queue to
synchronize and deduplicate time-based revalidations," and a Tag Cache (D1 or a
Durable-Object sharded tag cache).

**Durable Object queue** — docs:

> "The Durable Object Queue will send revalidation requests to a page when needed, and
> offers support for de-duplicating requests. By default there will be a maximum of 10
> instance of the Durables Object Queue and they can each process up to 5 requests in
> parallel, for up to 50 concurrent ISR revalidations."

Implementation
([`packages/cloudflare/src/api/durable-objects/queue.ts`](https://github.com/opennextjs/opennextjs-cloudflare/blob/da4b7fc8c89441a6f96205635fa70e329d1f0df7/packages/cloudflare/src/api/durable-objects/queue.ts)) —
this is a *true single-writer lock per shard*, since a Durable Object is
single-instance by definition:

- `ongoingRevalidations = new Map<string, Promise<void>>()` keyed by
  `MessageDeduplicationId` — "Ongoing revalidations are deduped by the deduplication id"
  (L22-L26). A message whose dedup id is already in flight is dropped (`revalidate()`
  returns early, L84-L88).
- A SQLite **sync table** inside the DO records `lastSuccess` per `host+url`; a message
  whose cached `lastModified` predates the last successful revalidation is dropped as an
  out-of-date regional-cache echo (`checkSyncTable`, L91, L295-L305).
- Failed routes go to `routeInFailedState` with exponential-ish retries via DO alarms
  (max 6 retries), and are not re-attempted by new messages meanwhile.
- Concurrency capped at `maxRevalidations` (default 5) per DO instance; paths are
  sharded across DO instances with the same `generateMessageGroupId` scheme inherited
  from `@opennextjs/aws` (`MAX_REVALIDATE_CONCURRENCY`, default 10 shards).
- The actual revalidation is a service-binding fetch back into the same Worker:
  `HEAD https://{host}{url}` with `x-prerender-revalidate: __NEXT_PREVIEW_MODE_ID` and
  `x-isr: 1`, success = `x-nextjs-cache: REVALIDATED` (L124-L136).

An optional `queueCache` wrapper reduces even the queue *sends*: per docs, it
"add[s] and verif[ies] a cache entry via the Cache API before dispatching a request to
the queue" (regional best-effort dedup in front of the DO).

**Cache interception** exists here too (serving ISR/SSG from cache without loading the
Next server); docs note it "does not work with PPR and is not enabled by default."

**Hard miss**: as on AWS, no cross-isolate coalescing of the blocking render itself —
the DO machinery only covers *background revalidation*, not first-render misses.
(Cloudflare's CDN-level "concurrent request coalescing" for cacheable content is a
platform property, not something this repo implements. UNVERIFIED whether it applies in
front of the OpenNext worker in default deployments.)

---

## 3. Next.js itself (`vercel/next.js`, self-hosted `next start`)

Coalescing is implemented in `ResponseCache` + `Batcher`, **per process, in memory**.

**Batcher** ([`packages/next/src/lib/batcher.ts#L25-L94`](https://github.com/vercel/next.js/blob/ab09c1f4b45d2ee316353ff4352d7efbbef396b2/packages/next/src/lib/batcher.ts#L25-L94)) — a
`pending = new Map<C, Promise<V>>()`; doc comment:

> "Wraps a function in a promise that will be resolved or rejected only once for a given
> key. This will allow multiple calls to the function to be made, but only one will be
> executed at a time. The result of the first call will be returned to all callers."

**ResponseCache** ([`packages/next/src/server/response-cache/index.ts`](https://github.com/vercel/next.js/blob/ab09c1f4b45d2ee316353ff4352d7efbbef396b2/packages/next/src/server/response-cache/index.ts)) uses two batchers:

- `getBatcher`, keyed `` `${key}-${isOnDemandRevalidate ? "1" : "0"}` `` (L108-L121) —
  all concurrent requests for the same path (miss *or* stale) within one process join a
  single `handleGet`. The key includes the on-demand flag so an on-demand revalidate
  "doesn't block normal requests" (L113-L115).
- `revalidateBatcher`, keyed by path (L123-L131) — a second coalescing layer so only one
  actual regeneration (`handleRevalidate` → `responseGenerator`) runs per key at a time.
  Both use `schedulerFn: scheduleOnNextTick` so the promise is registered in the pending
  map before any async work starts.

**Stale-while-revalidate flow** (`handleGet`, L318-L423): if a previous cache entry
exists, is not on-demand, and `isStale !== -1`, the caller's promise is resolved
*immediately with the stale entry* (`resolve(previousIncrementalCacheEntry)`), and if
`isStale` is truthy the function continues into `revalidate(...)` in the background
(hooked into `waitUntil`). Load-bearing comment (L347-L355):

> "`isStale === -1` signals that the entry is past its `expire` ... In that case we must
> NOT early-resolve with the stale value — instead we fall through to a blocking
> revalidation so the response returned to the user is fresh."

Also: `if (!previousIncrementalCacheEntry.isStale || context.isPrefetch) return` —
prefetch requests serve stale **without** triggering revalidation (this is the hook
OpenNext's `purpose: prefetch` workaround relies on).

**On a miss** (no previous entry): there is no early resolve; the render is blocking for
that request, and the batcher makes all concurrent same-key requests in the process
await the single render.

**Across processes: nothing.** The pending maps are process-local. With the default
file-system cache handler or a shared custom `cacheHandler` (Redis etc.), two Node
processes/containers can each regenerate the same path concurrently; Next.js provides no
cross-process lock or lease, and its self-hosting docs describe sharing the *cache
store* across containers, not serializing regeneration. (Absence of a mechanism
verified by inspection of `response-cache/` and `lib/incremental-cache/` at the pinned
commit; no doc claims otherwise — the stronger phrasing "Vercel-style collapsing is not
available self-hosted" is my inference, marked as such.)

**Minimal mode** (how Next runs *on* Vercel): `ResponseCache` skips the incremental
cache read (the platform serves cached content before the function is ever invoked) and
keeps a small LRU keyed by `pathname + invocationID` (or a 10s TTL fallback,
`NEXT_PRIVATE_RESPONSE_CACHE_TTL`) to dedupe the page + RSC-data sub-requests of one
revalidation invocation (L33-L104, L228-L260).

**On-demand header**: `checkIsOnDemandRevalidate` compares `x-prerender-revalidate` to
`previewModeId` ([api-utils/index.ts#L78-L108](https://github.com/vercel/next.js/blob/ab09c1f4b45d2ee316353ff4352d7efbbef396b2/packages/next/src/server/api-utils/index.ts#L78-L108));
when set, the response cache skips stale-serving and regenerates blocking (`handleGet`
checks `!context.isOnDemandRevalidate` before early-resolving).

**Failure backoff**: if regeneration throws and a previous entry exists, Next re-writes
the old entry with `revalidate` clamped to 3–30s "to prevent non-stop retrying"
(response-cache/index.ts L505-L529).

---

## 4. Vercel's documented behavior

Primary pages:
[vercel.com/docs/incremental-static-regeneration](https://vercel.com/docs/incremental-static-regeneration)
(last_updated 2026-04-30) and
[vercel.com/docs/incremental-static-regeneration/request-collapsing](https://vercel.com/docs/incremental-static-regeneration/request-collapsing)
(last_updated 2026-03-05).

**Stale-while-revalidate**:

> "It follows the stale-while-revalidate pattern: visitors get a fast cached response,
> and Vercel regenerates the page in the background based on a time interval or an API
> call you trigger."

> "Both execute in the background: visitors continue to get the cached version while
> Vercel generates the new content. Once the new version is ready, Vercel updates all
> representations of the path together. It purges HTML and data payloads atomically and
> propagates new content to all CDN regions through a global push pipeline."

**Request collapsing — one invocation per path, per region (not global)**:

> "**Automatic request collapsing**: When multiple requests hit the same uncached path,
> Vercel collapses them into one function invocation per region, protecting your backend
> during traffic spikes."

From the request-collapsing page (which names the stampede explicitly):

> "if multiple requests arrive while the initial function is still processing, the cache
> is still empty. Instead of triggering additional invocations, Vercel's CDN collapses
> these concurrent requests into the original one. They wait for the first response to
> complete, then all receive the same result."

> "Vercel also applies request collapsing when serving STALE responses (with
> stale-while-revalidate semantics), ensuring that concurrent background revalidation of
> multiple requests is collapsed into a single invocation."

> "Without request collapsing, each request would trigger a separate function
> invocation, which could overload the backend and slow down responses, causing a
> **cache stampede**."

So Vercel documents at-most-one *concurrent* revalidation per path **per region** —
both for cold misses (blocking, held requests) and for STALE background revalidation.
They do not claim a single global revalidation per path; the durable ISR cache is
single-region ("The ISR cache lives alongside your Function region"), which bounds
duplicate regeneration across regions, but a global per-path mutex is **not documented**
(UNVERIFIED / not claimed).

**Cache shielding / first request / expired entry**:

> "**Cache shielding**: On a CDN miss, Vercel reads from the ISR cache before invoking
> your function, reducing load on your origin."

> "If the CDN doesn't have a valid cached response, Vercel forwards the request to your
> Function region. If multiple requests hit the same uncached path at once, Vercel
> collapses them into a single invocation. Vercel then checks the durable ISR cache. If
> the cache has the content, Vercel serves it from the origin and replicates it back to
> the CDN. If not, Vercel invokes your function ..."

> "With ISR, Vercel knows a path is cacheable before the first request arrives. That's
> what enables request collapsing, durable storage, 300ms global purges, instant
> rollbacks, and path grouping."

**Failure semantics** (stale kept + 30s retry TTL):

> "If revalidation fails, Vercel keeps serving the existing cached content. ... When a
> failure occurs, Vercel preserves the stale content and sets a 30-second Time-To-Live
> (TTL), so it retries revalidation shortly after."

**Purge propagation**: "When you revalidate content, all caches across all regions
update within 300ms."

---

## 5. Comparative summary (mechanism table)

| Concern | OpenNext AWS | OpenNext Cloudflare | Next.js self-hosted | Vercel |
| --- | --- | --- | --- | --- |
| Who serves stale | CloudFront (`s-maxage` + SWR headers) and/or server fn / cache interceptor | Worker / cache interception | `ResponseCache` early-resolve | CDN |
| Background revalidation trigger | Server marks `STALE` → SQS FIFO message | Same → Durable Object queue | In-process, after early resolve (`waitUntil`) | Platform, in background |
| Per-path single-flight (stale) | SQS `MessageDeduplicationId` = md5(path+lastModified+etag), 5-min window; FIFO group = path-sharded ⇒ sequential per shard | DO `ongoingRevalidations` map by dedup id + SQLite `sync` table + failed-state map | `revalidateBatcher` pending map (per process only) | Request collapsing per region (documented) |
| Concurrency bound | `MAX_REVALIDATE_CONCURRENCY` shards (default 10) | 10 DOs x 5 (default 50) | unbounded across processes | not documented |
| Hard-miss coalescing | None in OpenNext; CloudFront per-POP request collapsing only; render blocks in each invoked Lambda | None in adapter (blocking render per isolate) | `getBatcher` per process; nothing cross-process | Collapsed to one invocation per region; held requests share the response |
| Revalidator → server call | HEAD + `x-prerender-revalidate: previewModeId` (+ `x-isr: 1`) | Same, via service binding | n/a (internal) | internal / on-demand API |
| Blocking-revalidate signal in Next | `x-prerender-revalidate` match ⇒ `isOnDemandRevalidate` ⇒ blocking, `x-nextjs-cache: REVALIDATED` | same | same | same (minimal mode) |
| Suppress Next's own bg revalidation | `purpose: prefetch` header workaround | inherited | n/a | minimal mode (cache handled by platform) |
| Tag store | DynamoDB (`tag, path, revalidatedAt`) | D1 or DO sharded tag cache | cache-handler-dependent | platform-internal |
| Failure handling | Failed SQS records returned for retry | Failed-state map + DO alarms, max 6 retries | rewrite old entry with 3–30s revalidate | keep stale + 30s TTL retry |

## 6. Verification status

Verified from primary sources: everything quoted above (OpenNext AWS/Cloudflare source
at pinned commits, opennext.js.org docs, vercel/next.js source at pinned commit,
vercel.com/docs pages, AWS SQS/CloudFront/Lambda docs).

**UNVERIFIED / inferences, flagged inline:**

- Vercel: whether any cross-*region* (global) per-path revalidation mutex exists — not
  documented; docs only claim per-region collapsing.
- Cloudflare CDN-level request coalescing in front of an OpenNext worker deployment.
- "Vercel-style collapsing is not available self-hosted" is an inference from the
  absence of any cross-process mechanism in the Next.js source, not a documented claim.
