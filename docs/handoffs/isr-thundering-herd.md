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
     └─ 2  isr-writer worker + DO          ocelhq-wvag.2  ✅ CLOSED, reviewed ×2, not pushed
         └─ 3  manifest projection         ocelhq-wvag.3  ✅ CLOSED, reviewed, not pushed  ✅ code complete, not pushed
             └─ 4  streams publisher       ocelhq-wvag.4
                 └─ 5  origin reads snapshot ocelhq-wvag.5
                     └─ 6  get drops BatchGetItem ocelhq-wvag.6
                         └─ 7  edge L0/L1   ocelhq-wvag.7
                             └─ 8  edge L2 lease ocelhq-wvag.8   ⛔ blocked on .9
```

Filed out of band: `ocelhq-wvag.9` (measure the L1 write-visibility window; **blocks `.8`**),
`ocelhq-wvag.10` (live e2e for the writer), `ocelhq-wvag.11` (destroy leaves per-build writer
DO instances behind), `ocelhq-wvag.12` (collapse the twice-derived projection key and
filename into `@ocel/next-cache`; needs a dist build on that package, so it touches the
Lambda and worker bundling — best done between PRs rather than inside one).

## Working method

Subagent-driven throughout: a fresh agent implements, a second reviews, a third applies
review fixes. The orchestrator does not write code. Each PR gets unit + e2e coverage; the
load/herd harness lands with PR 8 and is then run against the whole stack.

Agents own the mechanics end to end — claiming and closing bd issues, wiring dependencies,
labelling, branching, committing. None of that is a human's job. The only things that stop
the chain are pushing, creating PRs, and deploying to a real account.

**One agent per worktree at a time.** Two agents in this worktree once switched branches
under each other mid-run and a commit landed on the wrong branch. It was recovered, but
serialize work on a branch rather than parallelising into the same checkout.

## Environment preconditions

Cloudflare credentials were exported interactively in a prior session, so **a fresh session
starts without them** and any deploy or API call will fail in ways that read as a
misconfiguration rather than a missing credential:

```bash
export CLOUDFLARE_API_TOKEN=<token>
export CLOUDFLARE_ACCOUNT_ID=a1731fc73cb2bf6b2979c98033012ca8   # account "Ocel"
```

Putting those in the shell profile makes the problem go away permanently. Verify with
`curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $CLOUDFLARE_API_TOKEN" \
https://api.cloudflare.com/client/v4/user/tokens/verify` — expect `200`. Zones on the
account: `ocel.app`, `ocel.dev`, `ocel.site`. AWS is authenticated separately and was
working (`363236815301`).

`wrangler whoami` returns empty even with a valid token; use the API check above instead.

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

**PR 3 (`ocelhq-wvag.3`) — code complete, NOT pushed.**

Branch `isr-herd/03-manifest-projection`, rooted on PR 2. Serves decisions 4 and 5.

`next build` now emits a slim projection — route -> `{ rscHeaders, segmentHeaders }`, nothing
else — into every `.func` as `variant-headers.json`, alongside the launcher and `config.json`
the adapter already writes there. `set` reads it from the bundle and `carryForwardVariantHeaders`
is gone: a revalidation write is **one PUT and zero GETs**, and the non-atomic
read-modify-write went with it. Since PR 2 that GET was a writer round trip, so this removes
a network hop from every `set`, not just an R2 read.

The entry a build seeds and the projection a rewrite reseeds from are now derived by one
function over one grouping of a route's prerender outputs, so they cannot drift.

Nothing is fetched at runtime. Bundling is what makes a Lambda for build N unable to read
build M's headers; a cold-start fetch from S3 was considered and rejected in decision 5.

**PR 3 has been reviewed** (standards + adversarial spec/correctness), and the findings are
fixed on the branch. Four came out of it:

- **A corrupt projection failed *closed*, in code whose comment claimed fail-open.**
  `JSON.parse("null")` returns `null` without throwing, so the memo (`??=`) never took, the
  file was re-read on every `set`, and indexing `null` threw a `TypeError` inside `set`'s try
  *before* `background()` was scheduled — dropping the write. Every `APP_PAGE` route would
  have silently stopped caching for the life of the deploy, with no log. `loadVariantHeaders`
  now rejects anything that is not a non-null, non-array object, and the whole matrix
  (absent, unreadable, empty, malformed, `null`, array, string, number) is table-tested.
- **The projection's key space was derived twice** — the adapter's entry key and
  `cacheKey()` in `@ocel/next-cache` are the same transform, authored independently. Drift
  would make every lookup miss and quietly disable PPR, the exact failure the projection
  exists to prevent. Contract tests now pin both that pair and the twice-spelled filename.
  Collapsing them to one derivation needs a packaging change and is filed as
  **`ocelhq-wvag.12`** — see below.
- The acceptance criterion "segment prefetch verified" was asserted only as bytes on the
  entry; nothing drove a rewritten entry through `reconstructSegment`, the consumer that
  returns null and disables PPR without `segmentHeaders`. Now covered in `workers/nextjs`,
  both directions, and the negative was mutation-checked to confirm it has teeth.
- Comment/name cleanups: `PrerenderGroup.key` → `entryKey` (the name now carries what four
  lines of comment did), and a paragraph duplicated verbatim across two packages kept once.

Verified after the fixes: `packages/next-runtime` 157; `packages/lambda-entrypoints` 217 of
218 (the one failure is the known pre-existing `test/tag-clock.test.mts`, identical on the
base commit); `workers/nextjs` 525; `packages/next-cache` 34; `pnpm -r --no-bail typecheck`
clean except the four `examples/*` packages that fail on the base commit. No Go changed — a
`.func` is zipped whole, so the new file rides the existing artifact path.

**PR 2 (`ocelhq-wvag.2`) — code complete and reviewed twice, NOT pushed. Issue CLOSED.**

Branch `isr-herd/02-isr-writer`, rooted on PR 1. New account-level package
`workers/isr-writer/` (worker entry, per-deploy `IsrDeploy` Durable Object, registry SQL,
entry read/write, auth primitives), the Go deploy plumbing that provisions it, mints and
seeds each build's write secret, and prunes it on retirement, plus the Lambda-side client
that routes ISR entries through it.

**The second review happened, and it found three real defects.** All are fixed on the branch:

- **A cold-filled memo was born already spent.** `fromRegistry` hardcoded `refreshed: true`,
  but `refreshed` is defined as "already re-read *after a token failed against it*". So the
  one-re-read escape hatch never fired where it was designed to: redeploy the same buildId,
  and a warm isolate refused the freshly derived secret for up to 60 s while the Lambda
  logged a false *permanent* failure. Only the failure-triggered re-read sets the flag now.
  Cost is that a bad token at a cold isolate spends two DO calls instead of one, then the
  prefix is refused off the memo — the bound that matters still holds.
- **No in-flight coalescing on the registry read.** The memo stored resolved values, never
  the pending promise, so every concurrent request on a cold isolate issued its own RPC to a
  *single-threaded* DO. Sized for writes, this became a herd at the auth boundary once reads
  joined the hot path — in a PR whose whole purpose is removing herds. In-flight reads now
  share one round trip, and a rejected read is not cached.
- **An unknown prefix wrote durable storage, unauthenticated.** `authorized` reached the DO
  before verifying any credential, and `deployPrefix` checks only the *shape* of a name, so
  any junk bearer against a well-formed prefix materialized a DO whose constructor ran
  `ensureSchema` — a storage write. Varying the prefix created unbounded persisted objects,
  exactly the litter `ocelhq-wvag.11` exists to clean up. `ensureSchema` now runs only on the
  write path, `secretHash()` tolerates a missing table, and the memo map is capacity-bounded.

Two smaller ones: a misdirected read is no longer indistinguishable from a cold cache (the
worker marks an entry-miss 404; the Lambda warns on any other 404 and still misses), and the
credential primitives both account-level workers had copied are now one package,
`@ocel/worker-auth`.

Verified after the fixes: `pnpm -r --no-bail typecheck` clean across every source package
(the four `examples/*` failures are pre-existing — the dogfooded SDK is not built);
`workers/isr-writer` 42; `workers/deployments-store` 63; `packages/worker-auth` 7;
`packages/next-cache` 34; `workers/nextjs` 523; `packages/lambda-entrypoints` 207 of 208 (the
known pre-existing `test/tag-clock.test.mts`); `cloud/aws` and `cloud/edge` build and test
clean. Note `gofmt -l` flags `cloud/edge/cloudflare/cloudflare_test.go` — pre-existing drift
from `b17467f`, untouched by this stack.

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

**PRs 2 and 3 are both reviewed and their findings are fixed.** Nothing in the stack is
waiting on scrutiny. Branch `isr-herd/04-streams-publisher` off `isr-herd/03-manifest-projection`
and dispatch `ocelhq-wvag.4`.

Both review rounds landed real defects rather than polish — three in PR 2, one in PR 3 — and
in every case the failure was silent: a write dropped with no log, a herd at the auth
boundary, storage written for a caller who never authenticated, a projection that would miss
forever if two derivations drifted. Keep weighting later reviews toward *what fails without
saying so*, not toward what throws.

Three things `.4` should know before it starts:

- **It owns the last standing R2 credential's removal.** See "The credential is narrowed, not
  gone" above. Do not close `.4` without deleting `OCEL_CACHE_STORE_PARAM`, the
  `OCEL_ISR_STORE_*` injection and `snapshotObjectStore`. Until it lands, every deployed
  function holds a token that can write any object in the shared `ocel-edge-cache` bucket for
  every project.
- **It routes snapshot writes through the PR 2 worker**, so it inherits that worker's
  assumptions — including the ones still unproven against a real account (`ocelhq-wvag.10`).
- The write path's fail-open discipline is now established at two seams. Match it.

Two live threads to carry forward:

- `ocelhq-wvag.9` blocks `.8`. It needs a deploy, like `.1` did, so start it early rather
  than at PR 8.
- `ocelhq-wvag.4` now owns the last standing R2 credential's removal. See "The credential is
  narrowed, not gone" above.

## The one decision waiting on a human

`ocelhq-wvag.10` — live e2e for the writer — needs authorization, not just scheduling.
Running a throwaway probe on a zone route (PR 1) and standing up **account-level
infrastructure** are different things, and the second has not been approved.

What is unproven until it runs: the script uploading with its DO migration tag on a first
bootstrap, the R2 binding resolving to the real bucket, a deployed Lambda authenticating
against a seeded hash and landing an entry at the key the edge reads, a genuine R2 429
(only unit-tested against a fake throw), and `retireISRWriter` against a real prune.

That is a meaningful share of PR 2's risk, and it compounds: PR 4 routes snapshot writes
through the same worker, so a wrong assumption here propagates. The tradeoff is that
approving it puts a new account-level worker with a bucket-wide R2 binding on the Ocel
account before the stack that justifies it is complete.

Either answer is workable. It just should not be decided by an agent inferring consent from
the fact that credentials happen to be present.
