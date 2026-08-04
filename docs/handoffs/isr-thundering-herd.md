# Handoff — ISR thundering-herd remediation

Rolling handoff for epic `ocelhq-wvag`. Update the "Current position" section at the end
of each PR; leave the rest as the standing record.

## Where the spec lives

`bd show ocelhq-wvag` holds the full decision record: verified problem statement, the nine
research corrections that overturned parts of the original plan, fifteen numbered decisions,
standing assumptions, and out-of-scope items. **Read it before touching anything.** The
children `ocelhq-wvag.1` … `.8` each cite the decisions they serve.

Do not re-litigate the research corrections. They were established against Next.js 16.2.10
source, Cloudflare docs and AWS docs, and several of them invert the plan's original
instincts — most importantly that the build manifest fixes the *write* path, not the read
path, and that a shared coordinator DO is Cloudflare's named anti-pattern.

## Stack shape

Eight PRs, each rooted on the previous, first rooted at `main`. Dependency-derived order —
note this deliberately inverts the original 1a→1b→2a→2b sequencing, because PR 6 removes the
DynamoDB fallback and therefore cannot land before the publisher that becomes the sole
guarantor of invalidation.

```
main
 └─ 1  isr-herd/01-cache-api-spike        ocelhq-wvag.1   ✅ CLOSED (measured)
     └─ 2  isr-writer worker + DO          ocelhq-wvag.2  ✅ code complete, not pushed
         └─ 3  manifest projection         ocelhq-wvag.3
             └─ 4  streams publisher       ocelhq-wvag.4
                 └─ 5  origin reads snapshot ocelhq-wvag.5
                     └─ 6  get drops BatchGetItem ocelhq-wvag.6
                         └─ 7  edge L0/L1   ocelhq-wvag.7
                             └─ 8  edge L2 lease ocelhq-wvag.8   ⛔ blocked on .9
```

Filed out of band: `ocelhq-wvag.9` (measure the L1 write-visibility window; **blocks `.8`**),
`ocelhq-wvag.10` (live e2e for the writer), `ocelhq-wvag.11` (destroy leaves per-build writer
DO instances behind).

## Working method

Subagent-driven throughout: a fresh agent implements, a second reviews, a third applies
review fixes. The orchestrator does not write code. Each PR gets unit + e2e coverage; the
load/herd harness lands with PR 8 and is then run against the whole stack.

## Standing notes

**Writer retirement is bounded, not instant (PR 2).** The writer worker memoizes each
deploy's secret hash per isolate. `destroy` clears the memo only in the isolate that served
it, so an isolate that never handled the retirement keeps authorizing that build's writes
until its memo lapses — up to `MEMO_TTL_MS` (60s) in `workers/isr-writer/src/index.ts`. That
is the accepted bound, not an oversight: closing it means consulting the Durable Object on
every entry write, which is what the memo exists to avoid, and epic decision 6c mandates the
memo. Read decision 6d and commit `79900d5`'s message with that bound in mind — both are
worded as though retirement takes effect everywhere at once, and it does not.

## Current position

**PR 2 (`ocelhq-wvag.2`) — code complete, NOT pushed, issue in progress.**

Branch `isr-herd/02-isr-writer`, rooted on PR 1. New account-level package
`workers/isr-writer/` (worker entry, per-deploy `IsrDeploy` Durable Object, registry SQL,
entry read/write, auth primitives), the Go deploy plumbing that provisions it, mints and
seeds each build's write secret, and prunes it on retirement, plus the Lambda-side client
that routes ISR entries through it.

Verified on this branch: `tsc --noEmit` clean across every workspace package (`examples/hono`
fails, pre-existing and unrelated — the dogfooded SDK is not built); `workers/isr-writer`
43 tests over 4 files; `packages/lambda-entrypoints` 205 of 206 (the one failure is the
known pre-existing `test/tag-clock.test.mts:371`, identical on the base commit);
`workers/nextjs` 523 tests; `packages/next-cache` 34 tests; `wrangler deploy --dry-run`
succeeds at 8.01 KiB / 2.68 KiB gzipped; all seven Go modules build and `cloud/aws` +
`cloud/edge/cloudflare` tests pass.

**Nothing is deployed to Cloudflare.** The worker has never run on a real account, which is
what `ocelhq-wvag.10` exists for.

### The scope addition that came out of review

The PR as first built moved entry *writes* behind the worker and left `readEntry` going
direct to R2 — so the Lambda still held a bucket-scoped read+write R2 token, and the
credential-hygiene case the whole worker was justified on (decision 6) was unrealized. R2
tokens scope to a bucket and have no key-prefix grammar, so a token kept for reads can still
write every project's entries on the substrate.

**Entry reads now route through the writer too** (`GET /<isrPrefix>/entry?key=`, same auth
path, same per-isolate hash memo, same shared `entryObjectKey`). The deployed function holds
no standing R2 credential *for entries*. Two properties come with putting a read on the
serving path, and both are regression-tested:

- **The read fails open, unconditionally.** Next calls `get()` for every request to a cached
  route and does not wrap it, so every read failure — unreachable writer, timeout, 5xx,
  refused credential, refused key, unparseable body — degrades to a cache MISS, which makes
  Next render. A writer outage is slow, never broken. This is the single most important
  correctness property in the change.
- **No Durable Object round trip per request.** Reads resolve the secret hash from the memo
  the writes already warm (decision 6c).

### The credential is narrowed, not gone

The tag-clock snapshot publisher (`packages/lambda-entrypoints/src/next/use-cache-store.mts`)
still writes `tag-clock.json` into the adopted store directly, so `OCEL_CACHE_STORE_PARAM`,
the `OCEL_ISR_STORE_ACCESS_KEY_ID` / `OCEL_ISR_STORE_SECRET_ACCESS_KEY` injection and the
accessor behind them survive PR 2, narrowed to that one consumer (the accessor is renamed
`snapshotObjectStore` to say so). Epic decision 8 moves the publisher; **deleting them is
part of `ocelhq-wvag.4`'s completion**, recorded as a comment on that issue and at both Go
sites. Do not close `.4` without it — until it lands, every deployed function holds a token
that can write any object in the shared `ocel-edge-cache` bucket for every project.

## PR 1 is closed, and its numbers changed two later PRs

`ocelhq-wvag.1` is **CLOSED with real measurements** — five runs, zone-routed at
`probe.ocel.dev`, all reaching colo JNB. Full detail in
`docs/research/cloudflare-cache-api-spike.md`; the three results that matter downstream:

- **The colo cache is effectively fully shared, not isolate-local.** Cross-isolate hit rate
  was exact (`412/412`, `456/456`, `199/199`, `409/409`) in four runs and `2386/2388` in the
  fifth — two misses in 3864 cross-isolate reads across all runs, and neither was early. A
  per-colo L1 sentinel does what PR 7 assumes. **But suppression opens at ~200 ms, not at 0**:
  the earliest cross-isolate hit was 201–251 ms after the write.
- **TTL is honored exactly from 1 s to 60 s with no floor.** The 1/2/5/10/30/60 s brackets all
  contain the requested TTL, and the reported `age` reaches `ttl − 1` at every step. So
  `snapshotTtlSeconds = 10` in `workers/nextjs/src/tag-clock.ts` **holds as specced** and needs
  no change. Note Cloudflare returns `cache-control: max-age=14400` regardless; the honored
  lifetime is the one the probe measured, not the one the header claims.
- **Isolates per colo: 99, a lower bound that had not plateaued.** Connection-to-isolate
  affinity in Workers is undocumented, so the probe counts only the isolates its requests
  happened to reach.

### The epic's L2 sizing formula degenerates — `ocelhq-wvag.9` replaces it

Decision-adjacent, and PR 8 must not be designed off the old rule. The epic sizes L2 fan-in as
the §3 isolate count scaled by `1 − crossIsolateHitRate`. With the cache measured as shared
that is `99 × (1 − 2386/2388) = 0.083` — under one request, and exactly **zero** in the four
runs that hit rate `1`. A formula that answers "no fan-in at all" cannot size a lease.

The term that actually sizes L2 is the **write-visibility window**: the ~200 ms between
`cache.put` returning and the sentinel becoming readable from other isolates. Requests
arriving inside it are unsuppressable by L1 at any hit rate. `workers/cache-probe` writes once
and then reads, so it cannot measure this. `ocelhq-wvag.9` was filed to race N concurrent
writers into a cold key and count how many reach `origin()` first; **it blocks
`ocelhq-wvag.8`**. Size L2 as (arrival rate × that latency), bounded above by the per-colo
isolate count.

## Findings from PR 1 that affect later work

**`caches.default` is inert on `*.workers.dev`, and this is not just a probe concern.**
`cloud/edge/cloudflare/cloudflare.go` `deployApp` enables the workers.dev subdomain only when
an app declares no domains, otherwise attaching zone routes. So a **domainless Ocel deploy
has no colo cache at all** — the existing tag-clock Cache API front, the image-optimizer colo
tier, and the proposed L1 sentinel are all silent no-ops there.

Two consequences: PR 7's L1 must account for it, and PR 8's load harness must be zone-routed
or it measures the uncached path. This also supersedes the bd memory
`cloudflare-s-cache-api-caches-default-is-a`, which predates the custom-domain→routes switch
and claims every deployment lands on workers.dev.

## Standing constraints for every PR in this stack

- Nothing is pushed and no PR is created without explicit authorization. Commits are local.
- No backward-compatibility shims or migration paths for existing deploys — out of scope
  by decision.
- Do not touch `entry.cacheControl` persistence or the edge's preference for it over the
  manifest. It is load-bearing for correctness: Next's own `SharedCacheControls` override is
  process-local and non-durable, and the render-error clamp rewrites revalidate windows at
  runtime.
- Correct Next 16.2.10 method names: `updateTags(tags, durations?)` on the plural handler,
  `revalidateTag(tags, durations?)` on the singular. `expireTags` and `receiveExpiredTags`
  do not exist.
- Cloudflare Tiered Cache is deliberately not relied on — `originBlocking` sends a
  SigV4-signed request carrying `x-prerender-revalidate` specifically to bypass caching.

## Next step

Review PR 2, then branch `isr-herd/03-manifest-projection` off `isr-herd/02-isr-writer` and
dispatch `ocelhq-wvag.3`.

PR 3 deletes `set`'s prior-entry GET (`carryForwardVariantHeaders` in
`packages/lambda-entrypoints/src/next/cache-handler.mts`), sourcing `rscHeaders` /
`segmentHeaders` from a build-time manifest projection instead. Note that read is now a
writer round trip rather than a direct R2 GET, which makes deleting it worth slightly more
than the epic costed it at.

Two live threads to carry forward:

- `ocelhq-wvag.9` blocks `.8`. It needs a deploy, like `.1` did, so start it early rather
  than at PR 8.
- `ocelhq-wvag.4` now owns the last standing R2 credential's removal. See "The credential is
  narrowed, not gone" above.
