# ISR queue-deduplicated revalidation — implementation spec

Decision record + execution instructions for the work that follows `ocelhq-wvag.18`/`.19`.
Written 2026-08-05 against commit `63e35a1` (branch `isr-herd/13-deploy-concurrency-cap`).
Line numbers cite that commit; re-resolve them if the file has moved, but the named
functions/seams are the contract. Every factual claim in §1 was verified against installed
source or official docs on 2026-08-05; citations are in §12. **Nothing in this document is
left for the implementer to research.** Where a choice existed, it has been made and the
rationale recorded.

This document AMENDS the epic `ocelhq-wvag` decision record (new decisions 16–19, §2) and
supersedes nothing that has landed. `ocelhq-wvag.8` (lease DO) stays DEFERRED; this is the
architecture that replaces the need for it on the background path.

---

## 0a. AMENDMENTS — READ BEFORE ANY SECTION BELOW

This document landed verbatim in `321c6e5`, carrying none of the decisions the human took
on it the same day. Where an amendment and the body disagree, **the amendment wins**. Each
one is also applied inline at its own site, so a reader who arrives at a section directly
is not misled; this table is the index.

| # | Amendment | Sites |
| --- | --- | --- |
| A | The colo store's STALE skip is narrowed to **Lambda provenance**, not the header alone. `servedFromStore` stamps `x-nextjs-cache: STALE` on PRERENDER serves too, so the blanket rule deletes the colo tier for every stale route for the whole duration of a tag invalidation. Gate on `CACHE_STATUS !== "PRERENDER"`. | §2 (decision 18), §6.2 |
| B | The blocking-miss collapse (§7, `.28`) is **DEFERRED behind `.17`**. `missWaitBudgetMs = 3000` sits on the serving path and is asserted, not measured; `.17` measures the two numbers it must be sized from. Reversal: a measured hard-expiry fan-in per colo, plus a measured p95 render+store. | §7, §10 |
| C | `MessageRetentionPeriod` is **300**, not 3600. At 1h a wedged consumer accumulates ~12 stale echoes per route per hour, each with a distinct `lastModified` and therefore a distinct dedup id, and every one renders sequentially on recovery. | §3.1 |
| D | Trigger-secret hardening: SSE-KMS on the queue **and** the DLQ; an explicit no-log-the-raw-record rule in the handler; and **the host is not validated, it is resolved** — see the note below, which supersedes §5.2's regex and §4.2's `url` field. | §3.1, §4.2, §5.2, §5.3 |
| E | `.26` (suppression) lands **BELOW** `.25` (enqueue) in the stack. §10 asserts suppression must precede enqueue being live and then orders them the other way. Edges: `.24 → .23`, `.26 → .24`, `.25 → .26`, `.27 → .25`. | §10 |
| F | `OCEL_REVALIDATE_QUEUE_URL` is rendered **only when the revalidator function is rendered** — i.e. only when the artifact pin is present — not merely when the queue exists. Otherwise a deploy landing between `.25` and `.27` enqueues into a consumer-less queue, the thunk returns "landed", the sentinel re-arms, and the route silently stops revalidating until hard expiry. | §3.2, §5.3 |
| G | `VisibilityTimeout` is **300**, not 60: a batch of 10 at 10s per record is up to 120s of work (10 records × a 10s trigger budget plus, in the worst case of ten distinct deploys, a 2s record read; `platform/aws/functions/revalidator/src/limits.mts` sizes it and `test/limits.test.mts` asserts it), and at 60s the consumer DLQs records it already processed successfully. Also: `MaximumConcurrency` is a **`ScalingConfig` sub-property** in CloudFormation, not a top-level event-source-mapping property, and the document gives the Lambda **no function timeout at all** — it needs an explicit one. | §3.1, §5.3 |

### Amendment D in full: the message names no host

§4.2 gives the message a `url`, and §5.2 validates its host against
`^[a-z0-9-]+\.lambda-url\.[a-z0-9-]+\.on\.aws$`. Both are superseded.

The regex admits **any** AWS customer's Function URL, so a compromised edge key could
enqueue a message that exfiltrates the app's `bypassToken` plus a valid SigV4 signature to
an attacker's endpoint. An allowlist of exact hosts was the first replacement and is also
rejected — structurally, not stylistically. The consumer is rendered by
`cloud/aws/bootstrap` (one CloudFormation stack per account, at provider-install time);
the Function URLs it would have to permit are created by `cloud/aws/deploy/function.go`
(one `lambda.NewFunctionUrl` per `ManifestFunction`) on every app deploy, with ids nobody
can know at bootstrap time. The list renders empty and nothing refreshes it. Even with an
updater: CFN drift reverted by the next bootstrap, lost writes on concurrent deploys, and
Lambda's 4KB env cap at ~100 hosts.

So the message names no host at all. It carries `isrPrefix` and `routeId`, and the
consumer **resolves** the origin from the record the deploy itself writes:

```
s3://<OCEL_ASSET_BUCKET>/<isrPrefix>/origin.json
{ "v": 1, "functionUrls": { "<routeId>": "https://<id>.lambda-url.<region>.on.aws/" } }
```

— the same `routeId → Function URL` map `buildDeploymentRecord` already computes via
`appFunctionURLsByRoute`, written to the one place keyed by `isrPrefix` that the
consumer's own account can read. `routePath` is joined onto the recorded origin and the
result's origin compared back to it, so a route path that tries to be a URL cannot become
one, and there is no list to keep current.

This removes the *host* from the message; it does not remove the *key*. `isrPrefix` still
chooses which record is read, and round-two review of `.23` showed that a lie there is a
working exfiltration primitive on its own. `isrPrefix` is therefore validated as a key
prefix at parse time, and `.24` scopes the read grant to `*/origin.json`. See §5.3a — the
claim "the exfiltration class is impossible rather than validated" was too strong as first
written, and this section is the corrected version.

What this obliges, beyond `.23`:

- **`.24`**: `s3:GetObject` on `!Sub '${AssetBucket.Arn}/*/origin.json'` — scoped to the
  record's own name, NOT to the bucket — on the revalidator role, and
  `OCEL_ASSET_BUCKET: !Ref AssetBucket` in its environment (exactly as `publisher.go`
  renders it for the tag publisher).
- **`.24` (`cloud/aws/deploy`)**: a new write. After the app stack's outputs are read,
  `PutObject` `<isrPrefix>/origin.json` into the asset bucket with the document above. The
  Function URL is only knowable after `up`, which is why bootstrap cannot render it and
  the deploy must write it.
- **`.25`**: build the message shape in §4.2 as amended — no `url`, plus `routeId`.

---

## 0. What is settled and not re-litigated

- **L0 (per-isolate), L1 (colo sentinel), the admission jitter, and the below-tier seam
  are landed and stay.** `admitRefresh` order is: jitter draw (capped by remaining stale
  window) → `claimSentinel` → `askBelow` (`satisfiedFromBelow`) → render. The seam was
  renamed from `refreshedFromBelow` to `satisfiedFromBelow` in `4bcfec1`.
  `refreshSentinelTtlSeconds = 5`, `refreshBackoffSeconds = 30`, `admissionJitterMs = 1000`
  (`platform/edge/cloudflare/workers/entry/src/cache.ts:471,569,489`). Do not change any of these constants — the
  `.16` spike's two-way staleness clause pins them.
- **The queue dedupes renders; the tag publisher propagates staleness. Both stay.** The
  stream-driven tag publisher makes entries *become* stale without per-request DynamoDB
  reads (the read-path snapshot both origin and edge consult). The queue governs what
  happens *after* something is stale. Neither subsumes the other: removing the publisher
  removes tag invalidation itself; removing the queue removes render dedup. Their one
  interaction is deliberate: a tag raise makes routes stale via the snapshot, and the queue
  then drains the resulting revalidations lazily and at bounded concurrency — only routes
  that receive traffic get re-rendered.
- **SQS FIFO in the customer's AWS account, not Cloudflare Queues.** Cloudflare Queues has
  no deduplication of any kind (verified — its docs delegate dedup to the application
  layer), no FIFO groups, at-least-once delivery. Building dedup on Cloudflare means a
  Durable Object, i.e. rebuilding the deferred `.8` off-path. SQS FIFO has native content
  dedup, message groups, DLQ redrive, and sits next to the origin it protects, in the
  account Ocel's philosophy says infrastructure belongs in.
- **The consumer is a NEW account-level Lambda (`revalidator`), not a branch inside
  tag-publisher.** Disjoint IAM, disjoint alarms, disjoint release cadence (the publisher
  is a human-gated pinned artifact; coupling them re-cuts that release for every consumer
  change), disjoint poison/DLQ domains. It mirrors the tag-publisher *pattern* (zip,
  pinned artifact, bootstrap-rendered resources) without sharing its artifact.
- **Origin stays the write authority.** The consumer never touches R2/S3. It sends a
  signed trigger request; the Lambda renders and writes through its own cache handler →
  isr-writer → R2, exactly as `originBlocking` causes today. This is also the
  framework-agnostic choice: the consumer never understands any framework's entry format
  (§8).
- **Fail open, everywhere, toward today's behavior.** Enqueue failure → the edge falls
  back to `originBlocking` exactly as now. Queue outage is slower convergence, never a
  broken serve and never a suppressed refresh.

---

## 1. Verified facts this design rests on (do not re-derive)

Full citations in §12. The installed Next is exactly `next@16.2.10` (single copy in the
pnpm store; `platform/aws/functions/entrypoints` has no `next` dependency).

1. **`x-prerender-revalidate: <previewModeId>` forces a blocking on-demand render.** The
   response cache's serve-stale early-resolve is guarded on `!context.isOnDemandRevalidate`
   (`response-cache/index.js:198-204`); `checkIsOnDemandRevalidate` compares the header to
   the prerender-manifest `preview.previewModeId` (`api-utils/index.js:103-112`). The edge
   already sends this header on `originBlocking` (`platform/edge/cloudflare/workers/entry/src/index.ts:741-744`,
   value = `target.config.bypassToken`, which the adapter emits verbatim from the routing
   manifest).
2. **This does NOT prevent Next's own self-revalidation — that is a separate, real,
   currently-unsuppressed render source.** On a normal user request whose cache-handler
   entry is stale-but-servable, Next early-resolves the stale entry and continues the
   revalidating render in-process as a detached batcher promise, handed to `waitUntil`
   (`response-cache/index.js:188-207, 174-175`). Ocel's membrane implements `waitUntil`
   and drains it before invocation-complete (`membrane.mts:80-93`,
   `forward.go:48-61`), so these renders run to completion. Nothing in lambda-entrypoints
   or next-adapter fakes `lastModified` or a prefetch purpose; `get()` returns the stored
   `lastModified` verbatim (`cache-handler.mts:186-205`). The trigger header on the
   consumer's request cannot prevent *other* requests from spawning their own renders —
   they are different requests.
3. **The suppression hook exists and is exact:** `if (!entry.isStale || context.isPrefetch)
   return entry;` (`response-cache/index.js:201`), with
   `isPrefetch: req.headers.purpose === 'prefetch'` (`route-module.js:634`). A stale entry
   is served with NO revalidation when the request carries `purpose: prefetch`. The
   on-demand guard sits above it, so the consumer's trigger is unaffected by this header.
4. **Stale serves are detectable:** standalone (non-minimal) Next stamps
   `x-nextjs-cache: HIT|STALE|MISS|REVALIDATED` on SSG/ISR routes — app router
   (`app-page.js:954-962`) and pages router (`pages-handler.js:368-369`). On-demand
   success = `REVALIDATED`; Next's own `res.revalidate()` keys on exactly that header.
5. **A forced route render re-executes stale `use cache` entries.** Under
   `isOnDemandRevalidate` the use-cache wrapper skips `cacheHandler.get` entirely
   (`shouldForceRevalidate`, `use-cache-wrapper.js:1503-1506, 1282-1283`) and rewrites
   fresh entries. Tag-expired entries also force on-demand semantics via the
   `isStale === -1` promotion (`app-page.js:654-656`). So one queue-triggered render
   refreshes the route AND its embedded cache components — the cache-components concern is
   resolved in our favor.
6. **`isStale === -1` does NOT block the serving user request in 16.2.10.** It only
   upgrades the background regeneration to on-demand semantics. Hard-expiry blocking at
   the user's face is enforced by *our* edge/handler tiering, not by Next's response cache.
7. **HEAD runs the full render + cache-write pipeline;** only the final body write is
   skipped (`send-payload.js:76-79`; method gate `base-server.js:1310-1320` allows HEAD).
   Lambda Function URLs pass HEAD through. The consumer therefore uses HEAD.
8. **SQS FIFO:** 5-minute dedup interval, fixed (no attribute to change it); explicit
   `MessageDeduplicationId` overrides content-based dedup; `MessageGroupId` is mandatory
   on FIFO sends; same-group messages are delivered to Lambda in order and a failing
   group's messages are retried before new ones from that group; FIFO DLQ must be FIFO;
   queue name must end `.fifo`; 300 tps per partition (3,000 batched) without
   high-throughput mode — orders of magnitude above our load, so **normal mode, dedup
   scope = queue (default)**. `ReportBatchItemFailures` on FIFO is a *handler contract*:
   stop at the first failure in a group and return that item plus all unprocessed items.
   SigV4-signed HTTPS `SendMessage` works from any HTTP client (aws4fetch from Workers).
9. **Render-failure clamp:** when a revalidating render throws over a previous entry,
   Next rewrites it with `revalidate` clamped to [3,30]s (`response-cache/index.js:290-307`)
   — the origin has its own retry damping; the consumer's retry policy (§5) sits above it.
10. **The AWS query protocol the edge sends over is not being retired, and nothing about
    the send can fail silently.** The edge speaks the query protocol
    (`Action=SendMessage&Version=2012-11-05`, `content-type:
    application/x-www-form-urlencoded`) rather than the JSON protocol, because it hand-builds
    the request instead of carrying an SDK. AWS's own FAQ answers "Will AWS query protocol be
    deprecated?" with "AWS query protocol will continue to be supported," and the JSON
    protocol is a *client-side SDK* upgrade (`X-Amz-Target: AmazonSQS.SendMessage`,
    `application/x-amz-json-1.0`) — the FAQ's own downgrade instruction is to pin a previous
    SDK version, so there is no server-side migration to be caught by. The query POST is
    documented as exactly what `queueSender` builds, with `Content-Type` the only required
    HTTP header; `MessageGroupId`/`MessageDeduplicationId` are top-level parameters, which is
    where the code puts them. A FIFO send missing either id fails — every documented
    `SendMessage` error, and the common `MissingParameter`, is HTTP 400 — so `response.ok` is
    false and the caller renders through `originBlocking`. aws4fetch lists `content-type` in
    `UNSIGNABLE_HEADERS`, so the explicit header cannot cause a signature mismatch (SigV4
    needs only `host` + `x-amz-date`). This matters because an empty queue is the documented
    healthy state: a protocol the server refused would be indistinguishable from a queue with
    no work in it, which is why `queueSender` logs the refusing status.

---

## 2. Epic decision amendments (append to `ocelhq-wvag` as comments)

- **Decision 16 — queue-deduplicated revalidation.** Background revalidation renders are
  deduplicated globally by an account-level SQS FIFO queue in the customer account.
  `MessageDeduplicationId = sha256("${isrPrefix}:${routePath}:${lastModified}")` (one
  render per route per entry generation, the OpenNext-proven escape from the fixed 5-min
  window), `MessageGroupId = "${isrPrefix}:${routePath}"` (per-route serialization). An
  admitted edge refresh enqueues instead of rendering; enqueue failure falls back to
  `originBlocking`. L0/L1/jitter remain as send-rate bounds, no longer as the render
  bound.
- **Decision 17 — the consumer.** A new account-level `revalidator` Lambda (package
  `platform/aws/functions/revalidator`) consumes the queue and sends a SigV4-signed HEAD trigger to the
  origin. It mirrors the tag-publisher packaging/pin/release pattern; separate artifact,
  IAM, alarms, DLQ.
- **Decision 18 — self-revalidation suppression.** The edge adds `purpose: prefetch` to
  prerender-capable user-path forwards, making the edge/queue the only revalidation
  authority. The colo store declines to cache a STALE response **that came from the
  Lambda** — narrowed by amendment A; the blanket "any response stamped
  `x-nextjs-cache: STALE`" would delete the colo tier for every stale route, because
  Ocel's own `servedFromStore` stamps STALE on PRERENDER serves too.
- **Decision 19 — blocking-miss collapse is colo-scoped.** The miss path gains a per-colo,
  per-variant sentinel + bounded poll-wait (Vercel's held-requests model at colo
  granularity). Cross-colo blocking coordination stays out of scope pending `.17`
  measurements (same evidence bar as the `.8` deferral).

---

## 3. Component: the queue (cloud/aws)

### 3.1 Resources — `cloud/aws/bootstrap`, both substrate templates

Provision account-globally beside the tag-publisher block (wired where
`bootstrap.go:545,587` wires the publisher), production and preview classes each:

- `RevalidateQueue`: SQS FIFO. Name `ocel-revalidate.fifo` / `ocel-revalidate-preview.fifo`.
  `FifoQueue: true`, `ContentBasedDeduplication: false` (we always send an explicit id),
  default dedup scope (queue) and throughput (normal — do NOT enable high-throughput mode;
  it forces messageGroup-scoped dedup we don't need and our volumes are ~hundreds per
  stale event against 300 tps/partition), **`VisibilityTimeout: 300`** (amendment G — a
  batch of 10 at the handler's 10s per-record budget is up to 120s of work (see amendment G), and it must
  also outlast the function timeout below; at 60 the consumer DLQs records it already
  processed successfully), **`MessageRetentionPeriod: 300`** (amendment C — a revalidation
  older than the dedup window is worthless, and at 1h a wedged consumer accumulates ~12
  stale echoes per route per hour, each with its own `lastModified` and therefore its own
  dedup id, every one of which renders sequentially on recovery),
  `RedrivePolicy: maxReceiveCount 5` → `RevalidateDLQ`.
- `RevalidateDLQ`: SQS FIFO (mandatory for a FIFO source), 14-day retention.
- **SSE-KMS on both** (amendment D): every message carries the app's `bypassToken` in
  `x-prerender-revalidate`, so the queue and the DLQ are secret-bearing stores.
- Stack Output for the queue URL, following the `assetBucketOutput` pattern
  (`bootstrap.go:732-737`).
- **EdgeUser grant**: add `sqs:SendMessage` on `RevalidateQueue`'s ARN to
  `edgeUserResource` (`bootstrap.go:739-820`). Nothing else — the edge never receives or
  deletes.

### 3.2 Worker env plumbing — `cloud/aws/deploy`, `cloud/edge`

- New contract const `RevalidateQueueURLVar = "OCEL_REVALIDATE_QUEUE_URL"` in
  `cloud/edge/resolver.go:52-56`; rendered into the worker env by a
  `withRevalidateQueue(...)` helper mirroring `withCacheCoordinates`
  (`cloud/aws/deploy/production.go:566-577`). Value = the queue URL Output. The region is
  derived in TS from the URL host (`sqs.<region>.amazonaws.com`) — no second var.
- **Amendment F**: render the var only when the revalidator FUNCTION is rendered — i.e.
  only when the artifact pin is present — not merely when the queue exists. A deploy
  landing between `.25` and `.27` would otherwise enqueue successfully into a
  consumer-less queue, the thunk returns "landed", the L1 sentinel re-arms, and the route
  silently stops revalidating until hard expiry. This is a correctness requirement.
- Absent var ⇒ the edge behaves exactly as today (no enqueue path constructed). This is
  the wiring-seam rule from `.16`: a test must pin that the dep is constructed when the
  var is present (§9).

---

## 4. Component: the edge enqueue (platform/edge/cloudflare/workers/entry)

### 4.1 New dep

`CacheDeps` gains:

```ts
// Deduplicated deferral of an admitted background refresh. Returns true iff the
// message was accepted by the queue. Absent (no queue URL configured) or false
// means the caller must render via originBlocking as before — the queue is an
// optimization with a contract, never a gate.
enqueueRevalidation?: (message: RevalidationMessage) => Promise<boolean>;
```

Built in `resolveRouteDeps` alongside the existing deps ONLY when
`OCEL_REVALIDATE_QUEUE_URL`, `OCEL_EDGE_ACCESS_KEY_ID` and `OCEL_EDGE_SECRET_KEY` are all
present. Implementation: a second `AwsClient` (aws4fetch, `service: "sqs"`, region parsed
from the queue URL — the existing signing client is `service: "lambda"`
(`signing.ts:11,50-92`) and must not be reused), `POST` to the queue URL with
`Action=SendMessage`, `AbortSignal.timeout(1000)`. Any non-2xx, abort, or thrown fetch ⇒
`false`. No retry at the edge — the fallback IS the retry.

### 4.2 The message

AMENDED by amendment D: the message names no host, and gains `routeId`. This is the
shape `.25` builds and `platform/aws/functions/revalidator` parses (`src/message.mts`).

```ts
interface RevalidationMessage {
  v: 1;
  headers: Record<string, string>; // x-prerender-revalidate, x-ocel-entry,
                                   // x-forwarded-host, x-forwarded-proto ONLY
  // Success contract, framework-declared by the edge (§8):
  expect: { header: string; value: string } | null; // Next: {header:"x-nextjs-cache", value:"REVALIDATED"}
  // What the consumer resolves the origin from — the deploy, and which of its
  // functions serves this route. `routeId` is the key the worker already
  // dispatches by (`functionUrls[target.id]`).
  isrPrefix: string;
  routeId: string;
  // Dedup ingredients, also used for logging:
  routePath: string;         // a path, never a URL; joined onto the resolved origin
  lastModified: number;      // ENTRY_MODIFIED of the stale entry that admitted
  enqueuedAt: number;
}
```

`x-forwarded-host` STAYS in the headers even though the consumer now knows the origin.
The consumer resolves the *origin* — the Function URL — while the app's *public* hostname
is route knowledge only the edge has, and the rendered entry's absolute URLs depend on it.
It names no destination, so it carries no part of the trust decision above.

`MessageDeduplicationId = hex(sha256(`${isrPrefix}:${routePath}:${lastModified}`))`,
`MessageGroupId = `${isrPrefix}:${routePath}`` (≤128 chars — truncate the routePath tail
to a hash if longer; one pure exported function derives both, tested directly like
`admissionDrawMs` is).

### 4.3 Where it wires — the three admission call sites, NOT `admitRefresh`

`admitRefresh` (`cache.ts:623-646`) is untouched. The change is inside the `refresh`
thunks passed to it at the three sites (`cache.ts:698`, `index.ts:866-875`,
`index.ts:902-912` — the last was cited as 901-913, which is the enclosing `if` block, not
the call): each thunk becomes

```
run = async () => {
  if (deps.enqueueRevalidation && await deps.enqueueRevalidation(message)) return "landed";
  return currentBehavior();   // originBlocking → outcome, exactly as today
}
```

Consequences, all deliberate:

- **Enqueue-accepted maps to the "landed" outcome**, so `settleSentinel` re-arms the L1
  sentinel for `refreshSentinelTtlSeconds = 5`. The colo re-admits ~5s later; by then the
  consumer has normally rendered and `askBelow`/`satisfiedFromBelow` answers the next
  admission from R2 with zero renders. Convergence ≈ sentinel TTL + render, which is the
  pre-existing re-poll cadence. Duplicate enqueues from other colos in that window are
  absorbed by SQS dedup — that is the whole point.
- **`askBelow` stays ahead of the enqueue** (it already runs inside `admitRefresh` before
  `run()`): a colo that can be answered from R2 never sends a message.
- The `lastModified` in the message is the `modified`/`meta.lastModified` already in scope
  at each site (it is what the staleness verdict was computed from). Thread it into the
  thunk's closure; do not re-read the entry.
- The image tier and the miss path never reach these thunks and are untouched.

---

## 5. Component: the consumer (`platform/aws/functions/revalidator` + `cloud/aws`)

### 5.1 Package

New `platform/aws/functions/revalidator` (`@platform/aws-revalidator`), mirroring `platform/aws/functions/tag-publisher`
byte-for-byte in build shape: single-file esbuild ESM bundle via the same
`scripts/build-zip.mjs` pattern (fixed timestamps, sorted entries, reproducible
`dist/revalidator.zip`), released as GitHub asset `revalidator-v<version>`, pinned by
version + sha256 in a new `cloud/aws/bootstrap/revalidatorversion.go` (copy the runbook
comment from `publisherversion.go:29-34` — read the file, the range cited in §12 is the
const block, not the comment). Unpinned placeholder ⇒ bootstrap skips rendering the
function, exactly as the publisher did pre-`.14`.

### 5.2 Handler contract

Per SQS record: parse `RevalidationMessage` (reject unknown `v` → item failure; `routePath` must be a path, `isrPrefix` must be a key
prefix — dot-free segments, no separator, no traversal, no absolute key, nothing empty,
per §5.3a — and every header name must be an RFC 9110 token, since one that is not throws
inside `new Headers` at signing time and would be classified as a transient handler
error);
**resolve the origin** from `s3://<OCEL_ASSET_BUCKET>/<isrPrefix>/origin.json` and
`routeId`, memoized per `isrPrefix` for the invocation, and compose the trigger URL from
the resolved origin and `routePath` (amendment D — this REPLACES the host-validation regex
this section originally carried; do not ship a pattern, and do not ship an allowlist);
SigV4-sign (`service: "lambda"`, region from the resolved host, credentials from the
function role — the message carries NO credentials) and send **HEAD** with the message's
headers, signed along with `host`. An unreadable record is a transient item failure
(`origin-unavailable`); a record that does not answer for this route, or a `routePath`
that would leave the resolved origin, is a permanent one (`origin-unusable`). Success:

- `response.ok && message.expect === null` ⇒ success.
- `response.ok && header(expect.header) === expect.value` ⇒ success.
- `response.ok` but the expect header mismatches/absent ⇒ **success with a
  `RevalidateExpectMiss` structured log line** (the route may have become dynamic since
  the message was enqueued; re-driving it cannot help). Do not fail the item.
- Non-ok, timeout (10s `AbortSignal`), or thrown fetch ⇒ item failure.

**FIFO batch contract (verified semantics, §1.8):** process records in order; on the
FIRST failure in a message group, stop processing that group and include that record AND
every unprocessed record of the same group in `batchItemFailures`. Records of other
groups in the batch continue. A thrown handler ⇒ whole batch fails (avoid; catch
per-record). Retries are SQS redelivery (visibility 300s × maxReceiveCount 5) → FIFO DLQ.
This is the bounded backoff `.18` required, now centralized: an origin shedding 429s
pushes retries out on the visibility-timeout schedule instead of any tight loop.

### 5.3 Resources — `cloud/aws/bootstrap/revalidator.go`

Mirror `tagPublisherResources` (`publisher.go:181-347`): artifact via `ensureArtifact`
(fail-closed digest verify); IAM role with `sqs:ReceiveMessage/DeleteMessage/GetQueueAttributes`
on `RevalidateQueue`; `lambda:InvokeFunctionUrl` + `lambda:InvokeFunction` scoped by
the `ocel:app` resource tag **copied exactly from `edgeUserResource`'s condition block**
(note that block is account-wide over every Ocel-tagged function, not app-scoped — `.24`
either tightens it or records the acceptance); `s3:GetObject` on `${AssetBucket.Arn}/*/origin.json`
(scoped to the record's own name — see §5.3a) and `OCEL_ASSET_BUCKET: !Ref AssetBucket`,
for the origin resolution of amendment D; and
**an explicit function `Timeout`** (amendment G — the document sets none; the package
documents 150s in `platform/aws/functions/revalidator/README.md`, sized in `src/limits.mts` and
asserted there, and it must stay below the queue's 300s `VisibilityTimeout`).
ESM on the queue: batch size 10, `ReportBatchItemFailures`, `MaximumConcurrency: 10`
**nested under `ScalingConfig`** (amendment G — it is a `ScalingConfig` sub-property in
CloudFormation, not a top-level event-source-mapping property; rendered at the top level
it is silently ignored or rejected) — the global render-drain bound, deliberately small;
a mass tag invalidation drains at 10 concurrent renders, which is the epic's first-ever
cap on total origin pressure — plus OnFailure → DLQ. Alarms, one block, same style as the
alarm resources **inside** `tagPublisherResources` (`publisher.go:181-347`; the const block
`publisher.go:22-102` is where the periods and thresholds live, not the resources):

- `ApproximateNumberOfMessagesVisible` on the DLQ > 0 (5 min) — poison revalidations.
- `ApproximateAgeOfOldestMessage` on the queue > 300s — consumer wedged or origin down.
- Function `Errors` > 0 (5 min) — handler defects.

No `PolledEventCount`-style absence alarm: an empty revalidation queue is the healthy
steady state (unlike the publisher's stream, silence here is not a signal).

**Expect the DLQ alarm on the first rollout, once.** Every build already live when `.24`
lands has no `origin.json`. Each of its enqueued routes resolves `origin-unusable` through
five receives and reaches the DLQ, so the alarm fires immediately and stays lit until the
300s `MessageRetentionPeriod` drains and every live build has been redeployed. Written
down here so that the alarm's first *real* signal is not dismissed as rollout noise.

### 5.3a `origin.json`: the read scope and the write

Added after round-two review of `.23`, which demonstrated working token exfiltration
against the shipped code. Three facts composed: the edge holds `s3:PutObject` on
`!Sub '${AssetBucket.Arn}/*/fetch-cache/*'` (`bootstrap.go`, edge-user policy block) and
writes fully-controlled JSON bodies there (`cache-entrypoint.ts`, `fetchObjectKey`); the
consumer interpolated `isrPrefix` straight into the record's read URL; and `isrPrefix` was
validated only as a string. A `#` or `?` truncates the appended `/origin.json`, so the
message named an arbitrary key — a fragment never reaches the wire and `aws4fetch` signs
`url.pathname`, so the signature matched what S3 served, and the consumer's
`url.origin !== base.origin` check agreed with the *planted* origin.

`.23` closed two layers (parse-time `isrPrefix` validation; an anchored `.on.aws` Function
URL host check). `.24` owns the third, and it is the one that holds even if the parser
regresses. It takes **both** grants, not one:

- **`s3:GetObject` on `!Sub '${AssetBucket.Arn}/*/origin.json'`, never `/*`**, and
  **`s3:PutObject` on `!Sub '${AssetBucket.Arn}/*/fetch-cache/*.cache.json'`**, never the
  unanchored `*/fetch-cache/*` the edge user originally carried. IAM's `*` spans `/`, so
  the read pattern alone admits `<prefix>/fetch-cache/origin.json` — which the unanchored
  write pattern also admits. Anchoring both on a trailing literal is what makes them
  disjoint: no key can end in `/origin.json` and `.cache.json` at once. Relaxing either
  suffix re-opens the vector.

  That every key the edge *worker* writes already ends `.cache.json` is a property of
  `fetchObjectKey`, not of the grant, and the threat modelled here is a stolen edge
  *credential*. The IAM patterns are the mechanism; the worker's code is not.

  Because the separation is by suffix, `origin.json` does not move to a prefix of its own —
  `*/fetch-cache/*` matches under any leading prefix, so a relocation buys nothing.

- **The parser rejects a `fetch-cache` segment in `isrPrefix`** (`message.mts`), the second
  independent layer. A prefix ending `.../fetch-cache` needs no truncation trick at all:
  the appended `/origin.json` lands inside the edge's write region on its own.

And on the deploy-side write, two hard requirements, both about the epic's own signature
failure mode:

1. **The record must land before the build is cut over to serving.** A live build with no
   record has routes that enqueue and never revalidate.
2. **A `PutObject` failure must fail the deploy, loudly.** Swallowing it reproduces the
   epic's signature exactly: the edge enqueues, the thunk returns "landed", the sentinel
   re-arms, the consumer answers `origin-unusable` for every route of that build, and the
   deploy output said nothing.

---

## 6. Component: self-revalidation suppression (platform/edge/cloudflare/workers/entry + tests only)

Two halves, both required (Decision 18):

1. **`purpose: prefetch` on user-path forwards.** In the `forward(...)` construction
   (`index.ts:694-699` is the `render` thunk; the `forward`/`safeHeaders` seam is
   `723-739`): when the target
   is prerender-capable (the same condition that attaches `x-ocel-entry`) and the method
   is GET/HEAD, set `purpose: prefetch` on the outgoing origin request — overwriting any
   client-sent value (the inbound request is untouched; this is the edge→Lambda leg
   only). Never on `originBlocking` (it carries `x-prerender-revalidate`, whose guard
   sits above the prefetch guard — adding it there is dead weight and muddies intent).
   Never on BYPASS traffic (draft cookie, bypass token, middleware set-cookie, non-GET):
   those paths are not ISR-governed and must stay byte-identical.
2. **Colo store declines a STALE serve *from the Lambda*.** AMENDED by amendment A: the
   original wording — decline any response stamped `x-nextjs-cache: STALE` — is wrong and
   must not be built. `servedFromStore` (`cache.ts`) stamps `STALE` on Ocel's own
   PRERENDER serves out of R2 too, so the blanket rule deletes the colo tier for every
   stale route for the whole duration of a tag invalidation. Gate on **provenance**, e.g.
   `CACHE_STATUS !== "PRERENDER"`, not on the header alone. In `store()`'s storability
   check (`cache.ts:407-414`): a Lambda-provenance `STALE` is a stale serve the Lambda
   made under suppression — caching it would launder stale bytes into a fresh-looking
   colo entry for a full window (the exact trap OpenNext's rejected "fake lastModified"
   option creates). Skip the put; still serve the response. `HIT`/`REVALIDATED`/absent,
   and anything Ocel served from the store itself, store as today.

Why suppression at all: without it, every Lambda-reaching request on a stale-servable
entry (R2-incomplete entries, uncacheable variants, pages `_next/data`) spawns an
undeduped in-Lambda render (§1.2) outside the queue's control — precisely under origin
pressure. With it, the edge admission tiers + queue are the *only* revalidation
authority, which is what makes `.17`'s "renders per stale event" a meaningful number.

**Mandated side-effect gate:** extend `scripts/e2e-next/` with a golden comparison — the
same prerender-capable route fetched through a deploy with and without the header must
produce byte-identical bodies and identical headers modulo `x-nextjs-cache`/date. OpenNext
ships this workaround at scale, but their caveat ("could break if Next changes prefetch
behavior") becomes ours; the golden test plus the §12 citation is the tripwire, and the
suppression must be one boolean constant that can be reverted alone.

---

## 7. Component: blocking-miss collapse (platform/edge/cloudflare/workers/entry) — DEFERRED

**Do not build this section yet** (amendment B). `.28` is filed deferred with a dep edge
on `.17`. `missWaitBudgetMs = 3000` below sits on the SERVING path and is asserted, not
measured; `.17` already measures hard-expiry fan-in per colo and p95 render, which are
exactly the two numbers the budget must be sized from, and the same evidence bar deferred
`.8`. Decision 19 is not withdrawn — its acceptance of per-colo-only scope still stands;
only the build is sequenced behind the measurement. REVERSAL: a measured hard-expiry
fan-in per colo showing the collapse is worth a serving-path component, plus a measured
p95 render+store to size the budget from. The design below is preserved as-is for that
day; every constant in it is a placeholder until then.

In `colo()` (`cache.ts:707-755`), between the failed `inFlightFill` join and
`const pending = origin()` (line 739):

- Leader/follower election via the existing sentinel machinery on a NEW url space
  `https://miss.ocel/<variant target.key>` (variant-keyed — the joiner is served the
  entry, and the join-key-must-stay-the-variant rule from PR 7 applies verbatim).
  `claimSentinel` wins ⇒ leader: proceed to `origin()` exactly as today; delete the
  sentinel in a `finally` once the fill settles (do not wait for TTL).
- Losers ⇒ followers: poll `deps.cache.match(keyRequest)` every **250 ms** up to
  **3000 ms** (constants `missPollIntervalMs`, `missWaitBudgetMs` in `cache.ts`, beside
  the other constants, with the derivation comment: budget ≈ p95 render + store; the
  poll target is the colo entry the leader's registered fill writes). On a servable hit
  ⇒ serve it through the normal `serveOrRefresh` verdict path (NOT raw — tag staleness
  must still be evaluated). On budget exhaustion or any cache error ⇒ fall through to
  `origin()` and render — fail open, the pre-existing behavior, same degradation rule as
  every other tier.
- Sentinel TTL: **10 s** (`missSentinelTtlSeconds`) — a crashed leader must not suppress
  a colo's misses beyond one render budget.
- GET/HEAD prerender-capable targets only; the image tier and BYPASS traffic never enter
  this path. **No jitter here** — this is the serving path; the documented rule at
  `cache.ts:631-634` stands.

Effect: hard-expiry fan-in per colo drops from ~active-isolates×variants to ~variants;
cross-colo remains ~C by explicit decision 19 (Vercel accepts per-region; we accept
per-colo pending `.17`).

---

## 8. Framework-agnosticism (the contract, stated once)

The queue and consumer are framework-blind by construction: the *edge adapter layer*
(which already holds per-route `config` from the routing manifest) prepares the entire
trigger request — URL, force-render header (`x-prerender-revalidate` for Next; whatever a
SvelteKit adapter defines for its ISR implementation), and the success expectation
(`expect`, or null when a framework offers no receipt header). The consumer signs and
sends what it is handed and evaluates only the declared expectation. Adding a framework
adds zero consumer/queue changes. The origin-writes-the-store rule (Decision 16/§0) is
what keeps entry formats out of the shared machinery entirely.

---

## 9. Test plan

The standing bar applies: every **[M]** assertion is mutation-checked (break the
production line, watch the named test fail, restore). All `CacheDeps` built through
`test/cache-deps.ts`.

### platform/edge/cloudflare/workers/entry

- **[M]** An admitted refresh with `enqueueRevalidation` present and resolving `true`
  never calls `originBlocking`, and settles the sentinel "landed" (re-armed, not
  deleted).
- **[M]** `enqueueRevalidation` resolving `false`, rejecting, or timing out falls back to
  `originBlocking`; the outcome then follows the origin response exactly as today.
- **[M]** `askBelow` runs before any enqueue (satisfied-from-below sends no message).
- **[M]** `deps.enqueueRevalidation === undefined` reproduces current behavior
  byte-for-byte (the seam-left-unwired guard), AND `resolveRouteDeps` constructs the dep
  when `OCEL_REVALIDATE_QUEUE_URL` + both edge creds are present and omits it when any
  is missing.
- **[M]** Dedup-id/group-id derivation: pure function, direct assertions (same id for
  same `(isrPrefix, route, lastModified)`, different id when `lastModified` moves, group
  ≤128 chars on a pathological route).
- **[M]** The SQS client signs with `service: "sqs"` (assert via a captured request), and
  the send carries `MessageGroupId` + `MessageDeduplicationId` — a FIFO send without
  either is a runtime error (verified §1.8).
- Suppression: **[M]** `purpose: prefetch` present on the prerender-capable GET forward;
  **[M]** absent on BYPASS and on `originBlocking`; **[M]** `store()` skips a response
  carrying `x-nextjs-cache: STALE` **of Lambda provenance** (amendment A) and stores
`HIT`/absent, and anything Ocel served from the store itself.
- Miss collapse: **[M]** two concurrent misses in one colo (distinct isolates simulated
  via distinct deps sharing one cache) yield one `origin()` call, follower served the
  leader's entry through the verdict path; **[M]** follower past `missWaitBudgetMs`
  renders; **[M]** leader failure (origin throws) deletes the miss sentinel; **[M]**
  cache errors admit (inert cache ⇒ exactly today's per-isolate behavior); **[M]** an
  `.rsc` follower never receives an HTML leader's fill (variant keying).

### platform/aws/functions/revalidator

- Handler: **[M]** per-group stop-at-first-failure (batch with groups A,B where A's
  first record fails: A's failed + unprocessed records reported, B fully processed);
  **[M]** origin resolution: a `routeId` the deploy never recorded, and a `routePath`
  that would leave the recorded origin, are item failures that never fetch the origin
  (amendment D — there is no host to validate); **[M]** the record is read once per
  `isrPrefix` per invocation and re-read on the next; **[M]** a record with no
  `MessageGroupId` does not stop the records after it; **[M]** a skipped record is
  logged, not only reported; **[M]** the default trigger and record-read budgets;
  **[M]** success requires `expect` match when declared; **[M]** ok-with-
  mismatched-expect logs and succeeds; **[M]** HEAD method and header passthrough
  (captured request); **[M]** unknown `v` → item failure.

### cloud/aws (Go)

- Template render: queue + FIFO DLQ + redrive present in both substrate classes; EdgeUser
  policy contains `sqs:SendMessage` on the queue ARN and nothing broader; the revalidator
  role's invoke grant carries the `ocel:app` tag condition verbatim; ESM has
  `ReportBatchItemFailures` + `MaximumConcurrency: 10`. **[M]** on the unpinned-artifact
  skip path (mirror the publisher's test).

### e2e (live substrate, `363236815301`, human-authorized like `.15` was)

1. Seed → make a route stale → drive one edge request → assert exactly one queue message
   (dedup: drive requests from two clients, still one), consumer renders, R2 entry's
   `lastModified` advances, next edge admission refills without a render.
2. The §6 golden byte-comparison.
3. Poison message reaches the FIFO DLQ after 5 receives; DLQ alarm fires. No message can
   name a host, so drive it with a permanently unresolvable record instead: enqueue a
   `routeId` this build never recorded (or point `isrPrefix` at a retired build, whose
   `origin.json` is gone), confirm each receive logs `origin-unusable` and fetches no
   origin, and confirm the message lands in the DLQ.

---

## 10. PR decomposition (stack order, each its own bd child of `ocelhq-wvag`)

File as `.23`–`.28` with `bd dep add` edges as listed; each description must cite the
decision (§2) it serves, per the epic-filing rule.

1. **`.23` — platform/aws/functions/revalidator** (consumer + zip/release machinery + unpinned
   `revalidatorversion.go`). No deps. Unit tests §9.
2. **`.24` — cloud/aws queue + consumer resources + EdgeUser grant + worker env
   plumbing.** Depends on `.23` (artifact name/env contract). Renders inert until the pin
   lands (`.27`).
3. **`.26` — suppression + Lambda-provenance STALE-store-skip + golden e2e.** AMENDED by
   amendment E: this lands **BELOW** `.25`, not above it. The original ordering asserted
   in the same sentence that suppression must precede enqueue being live and then put it
   second; landing it first is what keeps any live substrate from ever running the queue
   with the in-Lambda self-revalidation leak open. Depends on `.24`. Build the STALE skip
   as amendment A narrows it, not as §6.2 originally worded it.
4. **`.25` — edge enqueue path** (deps seam, SQS signing, three thunks, fallback, the
   §4.2 message as amended). Depends on `.26`. Behind the env var: absent ⇒ no behavior
   change.
5. **`.27` — human gate: cut `revalidator-v0.0.1`, pin version+digest, live e2e** (the
   §9 e2e list; extends the still-open `.10` run). `.14`-class release decision — an
   agent prepares, a human authorizes.
6. **`.28` — blocking-miss collapse** (§7). **DEFERRED behind `.17`** (amendment B): it is
   not parallel with `.25`, and `missWaitBudgetMs` may not be sized from anything other
   than a measurement. Edge: `.28 → .17`.
7. **`.17` (existing)** then measures the whole stack: renders per stale event (target:
   ~1 with the queue live), hard-expiry fan-in per colo (target: ~variants, not
   ~isolates×variants), measured `C`, and enqueue volume vs L1's predicted `C·E`.

Conservative profile rules apply: nothing pushed, no PRs created, no live deploys without
explicit authorization; `.27` is explicitly a human gate.

---

## 11. Out of scope (checked against the epic's list)

- Cloudflare Queues (rejected on verified absence of dedup, §0) and any DO-based queue.
- Reviving `ocelhq-wvag.8` — stays deferred behind `.17`'s measurements; its reversal
  conditions are unchanged.
- Cross-colo coordination of blocking misses (Decision 19 records the acceptance).
- Proactive (push) revalidation on tag raise — the queue is demand-driven by design;
  warm-rebuild-on-invalidate is a separate future issue if ever wanted.
- Tiered Cache, compat shims, image cache paths, `revalidateTags` read-your-own-writes —
  all per the epic's standing list.
- Changing L0/L1/jitter constants (pinned by the `.16` spike's staleness clause).

---

## 12. Verification appendix (all checked 2026-08-05)

**Installed Next** (`node_modules/.pnpm/next@16.2.10_.../node_modules/next/dist`):
on-demand guard `server/response-cache/index.js:198-204`; `checkIsOnDemandRevalidate`
`server/api-utils/index.js:103-112`; header const `lib/constants.js:265-266`; previewModeId
generation `build/preview-key-utils.js:43-49` (random per build, 14-day build-cache reuse);
self-revalidation detached-batcher + waitUntil `server/response-cache/index.js:174-175,
188-207`, `lib/batcher.js:46-60`; prefetch guard `server/response-cache/index.js:201` with
`server/route-modules/route-module.js:634`; `x-nextjs-cache` emission
`build/templates/app-page.js:954-962`, `server/route-modules/pages/pages-handler.js:368-369`;
`res.revalidate` keying on REVALIDATED `server/next-server.js:939`; use-cache force-
revalidate `server/use-cache/use-cache-wrapper.js:1282-1283, 1503-1506`; `isStale === -1`
promotion `build/templates/app-page.js:654-656` (no user-facing block in this version);
HEAD full-pipeline `server/send-payload.js:76-79`, `server/base-server.js:1310-1320`;
error clamp `server/response-cache/index.js:290-307`.

**Ocel** (at `63e35a1`): membrane waitUntil `platform/aws/functions/entrypoints/src/shared/
membrane.mts:80-93`, `cloud/aws/cmd/lambdanode/bootstrap/forward.go:48-61`; cache
handler real lastModified `platform/aws/functions/entrypoints/src/next/cache-handler.mts:186-205`;
originBlocking + signing `platform/edge/cloudflare/workers/entry/src/index.ts:741-744`, `platform/edge/cloudflare/workers/entry/src/
signing.ts:11,50-92`; forward/safeHeaders seam `platform/edge/cloudflare/workers/entry/src/index.ts:694-699` (the `render` thunk),
`723-739`; admission machinery `platform/edge/cloudflare/workers/entry/src/cache.ts:471,489,525-533,
549,551-557,569,576-596,601-607,623-646`; three sites `cache.ts:698`, `index.ts:866-875,
902-912`; miss path `cache.ts:707-755`; publisher pattern `cloud/aws/bootstrap/
publisher.go:22-102,118,120-140,181-347`, `publisherversion.go:35-44`, `artifact.go:105-181`;
EdgeUser `bootstrap.go:44-49,739-820`, `edge.go:161-235`; env plumbing
`cloud/aws/deploy/production.go:566-577`, `cloud/edge/resolver.go:33-56`.

**AWS docs**: FIFO dedup interval + MessageDeduplicationId semantics
(SQSDeveloperGuide/FIFO-queues-exactly-once-processing.html, APIReference/API_SendMessage.html);
high-throughput requires messageGroup dedup scope (APIReference/API_CreateQueue.html);
throughput quotas (SQSDeveloperGuide/quotas-messages.html); FIFO DLQ must be FIFO +
`.fifo` suffix (API_CreateQueue.html); Lambda FIFO ESM ordering/scaling/maxConcurrency
(lambda/latest/dg/services-sqs-scaling.html); ReportBatchItemFailures FIFO handler
contract (lambda/latest/dg/services-sqs-errorhandling.html); Function URL HEAD support
(lambda/latest/dg/urls-invocation.html); SigV4 SendMessage over plain HTTPS
(API_SendMessage.html); query protocol "will continue to be supported" + JSON protocol as a
client-side SDK upgrade with a pin-the-previous-version downgrade
(SQSDeveloperGuide/sqs-json-faqs.html, sections "Will AWS query protocol be deprecated?",
"How do I get started with AWS JSON protocols for Amazon SQS?", "What if I am already on the
latest AWS SDK version, but my open sourced solution does not support JSON?"); query POST
shape + "Only the `Content-Type` HTTP header is required"
(SQSDeveloperGuide/sqs-making-api-requests.html); JSON protocol headers `X-Amz-Target:
AmazonSQS.SendMessage` / `application/x-amz-json-1.0`
(SQSDeveloperGuide/sqs-making-api-requests-json.html); `MessageGroupId` /
`MessageDeduplicationId` as top-level parameters and "If you do not provide a
`MessageGroupId` when sending a message to a FIFO queue, the action fails"
(API_SendMessage.html — note the page names no error *code* for the omission; `MissingParameter`
is HTTP 400 in APIReference/CommonErrors.html, and every error listed on API_SendMessage is
400). `content-type` in aws4fetch's `UNSIGNABLE_HEADERS`:
`platform/edge/cloudflare/workers/entry/node_modules/aws4fetch/dist/aws4fetch.esm.mjs:18-28`.
**Cloudflare Queues has no dedup** (developers.cloudflare.com/
queues/reference/delivery-guarantees/ — application-layer idempotency recommended;
/queues/platform/limits/).

**Prior art**: `docs/research/isr-herd-prior-art-opennext-vercel.md` (pinned-commit
citations for OpenNext's SQS dedup + prefetch workaround and Vercel's per-region
collapsing + cache shielding).
