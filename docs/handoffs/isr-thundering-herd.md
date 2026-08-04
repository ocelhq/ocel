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
         └─ 3  manifest projection         ocelhq-wvag.3  ✅ CLOSED, reviewed, not pushed
             └─ 4  streams publisher       ocelhq-wvag.4  ✅ CLOSED, reviewed, not pushed
                 └─ 5  origin reads snapshot ocelhq-wvag.5  ✅ CLOSED, reviewed, not pushed
                     └─ 6  get drops BatchGetItem ocelhq-wvag.6  ⛔ blocked on .13, .14
                         └─ 7  edge L0/L1   ocelhq-wvag.7   ✅ CLOSED, reviewed, not pushed
                             └─ 8  edge L2 lease ocelhq-wvag.8   ⛔ blocked on .9
```

PR 7 was branched off **PR 5**, not PR 6, because `.6` is gated on two human decisions. Its
issue was closed with `--force` past that dependency edge. **It will need rebasing once `.6`
lands ahead of it** — they touch different files (`.6` is the origin Lambda, `.7` is the edge
worker), so the rebase should be mechanical.

Filed out of band: `ocelhq-wvag.9` (measure the L1 write-visibility window; **blocks `.8`**),
`ocelhq-wvag.10` (live e2e for the writer), `ocelhq-wvag.11` (destroy leaves per-build writer
DO instances behind), `ocelhq-wvag.12` (collapse the twice-derived projection key and
filename into `@ocel/next-cache`; needs a dist build on that package, so it touches the
Lambda and worker bundling — best done between PRs rather than inside one). Outside the epic,
`ocelhq-uroj` (the edge user's account-global `Query`/`BatchGetItem` grants, dead before this
stack began; see PR 5).

**Two of those now block `.6`, deliberately** — it removes the DynamoDB fallback and makes the
stream publisher the sole guarantor of invalidation fleet-wide, so neither can be forgotten:

- `ocelhq-wvag.13` — the heartbeat **staleness** alarm, deferred out of PR 4. PR 4 ships the
  heartbeat and a DLQ-depth alarm, which catches a publisher that *errors*. It does not catch one
  that is alive but wedged — not erroring, not filling the DLQ, just not advancing. That needs a
  metric source, and the repo has none.
- `ocelhq-wvag.14` — the human gate to cut and pin the tag-publisher release artifact, mirroring
  `ocelhq-pf6q.13` for the image optimizer. Both pin constants ship empty, so bootstrap renders
  **no** publisher until someone cuts it.

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

**PR 7 (`ocelhq-wvag.7`) — code complete and reviewed, NOT pushed. Issue CLOSED.**

Branch `isr-herd/07-edge-l0-l1`, rooted on **PR 5** (see the stack shape — it jumps the gated
`.6`). Serves decisions 9 and 12. Two commits, both in `workers/nextjs`: nothing else in the
repo is touched, and no Go changed.

Two verified herds are closed:

- **Variants no longer each start their own render.** Background refresh admission moved from
  the full variant key to the route (`buildId:routePath`), which `index.ts` already computed
  for the interception paths and which now rides on `CacheTarget.refreshKey`. One origin render
  rewrites a route's whole entry — html, RSC and every segment — so route-scoped admission
  refreshes every variant. **Storage stays variant-keyed**; `target.key` is still what is read,
  written and joined.
- **Cold-colo misses coalesce.** The fill is registered *before* `await origin()`, so a second
  miss on the same entry joins it and is served the entry the leader wrote instead of issuing
  its own render.

**L1 is a 5 s sentinel on the colo-shared Cache API** (`https://refresh.ocel/<key>`), which
carries the admission decision across isolates and past the in-flight window, at all three
refresh sites (colo stale, PPR stale, interception stale). It is consulted only when the caller
named a route: the image tier names none — an image is invalidated by its content hash
changing, not by a stale route — and keeps its per-isolate dedupe untouched.

Four things worth knowing:

- **The join key must stay the variant, and this is the sharpest correctness edge in the PR.**
  A joiner is answered with the leader's entry, so joining across variants would answer an
  `.rsc` request with HTML. Route-keyed admission applies only to *background* refreshes, whose
  response nobody serves. Both halves are commented at the sites and tested.
- **Every cache error admits.** `caches.default` is inert on `*.workers.dev` (PR 1's finding),
  so a domainless deploy sees every `match` miss and every `put` discarded — and degrades to
  exactly the old per-isolate dedupe rather than to a suppressed refresh. Asserted against an
  inert cache and against one whose `match`/`put`/`delete` all throw.
- **A joined follower now reports `x-ocel-cache: HIT` where it reported `MISS`.** It was
  answered from this colo's cache, so the header is honest, but dashboards reading MISS as "the
  origin rendered for this request" will shift.
- **The acceptance criteria are looser than they read**, deliberately, and the granularity is
  recorded on the issue: miss collapse is per *isolate* (a blocking miss has nothing to serve
  while it waits, so a sentinel cannot suppress it — that is `.8`'s job), and route collapse is
  "roughly one per route per TTL", bounded by the Cache API having no CAS and by a render
  longer than the TTL being re-admittable mid-flight. N → few, not N → 1, per the issue.

### The review found one defect, and it was silent — the fifth round running

Two independent reviews (standards + spec, the spec one weighted at the silent-failure classes
that have produced every real finding in this stack). The spec review first *cleared* the four
things most likely to be wrong — the join key is injective, the `.then` ordering that keeps
`clone()` ahead of the body being consumed holds even when the promise is already settled, the
`inFlight` map cannot collide now that it holds both route and variant keys, and every `catch`
admits — and then found the one that mattered:

- **A refresh that failed without throwing kept the sentinel.** The record was released only on
  a throw, and *none* of the three refresh paths throw on a failed render: `store()` drops a
  non-200 silently and the PPR path only cancels a body. An origin 500 therefore left the claim
  standing and stopped the route refreshing colo-wide for a whole TTL, with no signal — while
  the code's own comment claimed to have handled exactly that. An admitted refresh now reports
  whether it **landed** (the origin answered ok), and a refresh that did not land releases.
  Storing the bytes is deliberately not the test: on the interception path the render's real
  effect is the Lambda rewriting R2, and a 200 this colo cannot store still did that work.

Three quality findings were applied alongside it:

- **The suppression window shrank by the render duration** — the claim was taken before the
  render, so a 2 s render left siblings suppressed for 3 s of a 5 s TTL, and the window was
  smallest exactly when renders were slowest. A landed refresh re-puts the record, so the TTL
  runs from completion.
- **The sentinel url had three spellings**, two of them in tests, so a change to the derivation
  would have left both green and wrong — the same twice-derived-key-space class PR 3's review
  found. Derived once and exported now.
- `lookUp` named a call that can admit a background refresh and is made twice, once either side
  of the join, which is precisely where that matters; and the admit branch restated its own
  condition in a `??`.

Two bounds were reviewed and **deliberately not fixed**: a follower of a slow leader whose
response turns out unstorable pays the leader's latency plus its own render (the accepted cost
of coalescing, versus a timeout carrying its own failure mode), and a render longer than the
TTL can be re-admitted mid-flight. Both are commented at the site.

Verified after the fixes: `workers/nextjs` 545/545 (was 531 before this PR), `pnpm typecheck`
and `pnpm build` (`wrangler deploy --dry-run`) clean on the package; `pnpm -r --no-bail
typecheck` clean except the four pre-existing `examples/*` packages. Every new assertion was
mutation-checked — including reverting the registration to after the `await`, which fails with
`TypeError: Body has already been used`, i.e. the clone-ordering invariant breaking rather than
an assertion merely not matching.

**PR 5 (`ocelhq-wvag.5`) — code complete and reviewed, NOT pushed. Issue CLOSED.**

Branch `isr-herd/05-origin-reads-snapshot`, rooted on PR 4. Serves decision 11. Small: three
commits, one TypeScript, one Go, one review cleanup.

The origin's tag clock no longer touches the GSI. `UseCacheStore.queryTagRecords` is replaced
by `readTagSnapshot(etag: string | null)`, one ETag-conditional `GetObject` of the snapshot
PR 4's publisher started writing, answering `{status:"fresh", records, etag}` /
`{status:"unchanged"}` / `{status:"unusable"}`. A cold instance reads one object instead of
paging a partition; `ClockState.cursor` is gone and an opaque `etag` took its place. The 2 s
attempt throttle, the in-flight join, the silent catch and `observe()`'s upward merge are
untouched, so invalidation lag is unchanged.

Three things worth knowing:

- **`unusable` folds absent and unreadable into one answer, and it fails closed.** No
  `hasSynced`, no records touched, no throw. A snapshot that reads with *zero* records is a
  different thing entirely and does sync — that distinction is the whole of the fail-closed
  property, and both halves are tested.
- **This is the first reader of the S3 copy.** Until now nothing compared it to the R2 one, so
  a divergence there was invisible. It is now load-bearing for every origin `use cache` read.
- **The clock's fingerprint moved** from `OCEL_STATE_TABLE_INDEX` to `OCEL_ISR_BUCKET` +
  `OCEL_ISR_PREFIX` — the snapshot's identity is what a shared clock must agree on now.
  `OCEL_STATE_TABLE_INDEX` is unset by the deploy and read by nothing.

`dynamodb:Query` on `/index/gsi1` is gone from the **function's** policy. The **edge user's**
identical grant in `bootstrap.go` was deliberately left alone and filed as **`ocelhq-uroj`**:
that key's only DynamoDB call is `UpdateItem`, so its `Query` *and* its `BatchGetItem` were
both already dead before this PR — a separate audit, on an account-global template that
re-bootstraps every account, and best done after PR 7/8 settle the edge's read paths. The
index itself stays; the stream publisher is projected through it.

### The review found no defects — the first round in this stack that did not

Two independent reviews (standards + spec, the spec one weighted at the silent-failure classes
that produced all eleven prior findings). Both came back clean on behaviour. Mutation-checked
rather than asserted: deleting the `unusable` guard fails the never-synced test, and deleting
the etag assignment fails the conditional-request test. The 304 path was checked against the
shape the JS SDK actually returns — `NotModified`/`$metadata.httpStatusCode === 304`, tested
*before* the 404 branch, since a 304 read as a failure silently stops the clock and a 304 read
as `unusable` silently disables the remote tier.

Four quality findings were applied in `57b92c5`:

- **`drain()` was named for the deleted paging model** and now issues one conditional GET.
  Renamed `startSync()`, with the comment it made redundant deleted and only the non-obvious
  half kept — that the attempt is stamped *before* the read, which is what bounds the throttle
  to attempts rather than successes. The same dead vocabulary (`index`, `indexed`, "a drain")
  was cleaned out of the tests.
- The deleted production `TagRecordRow` had been **reborn twice in test scope**; it is now one
  shared `test/tag-rows.mts`.
- **`readableSnapshot` claimed to take a `TagSnapshot`** while its only untrusted caller hands
  it arbitrary bytes off the network — it type-checked solely because `JSON.parse` returns
  `any`. Its parameter is `unknown` now and it rejects non-objects explicitly, which deleted
  two casts elsewhere.
- One test named a property it could not falsify (records are only written on the `fresh`
  branch, so the `unusable` guard cannot affect them). Deleted as redundant; its sibling has
  the teeth.

Verified after the fixes: `packages/lambda-entrypoints` 197/197 — the long-standing failing
case died with the paging mechanism it tested, so the suite is green for the first time in this
stack; `packages/next-cache` 42; `packages/tag-publisher` 15; `workers/nextjs` 529;
`workers/isr-writer` 70; `pnpm -r --no-bail typecheck` clean except the pre-existing
`examples/*`; `cloud/aws` builds and tests clean.

Two `pnpm -r test` failures are **pre-existing and unrelated** — confirmed by running both
suites at `c887a83`, where they fail identically: `@repo/api` (9 files) and
`@ocel/provider-aws` (`Cannot find package 'ocel/config'` — the dogfooded SDK is not built,
the same cause as the `examples/*` typecheck failures).

**PR 4 (`ocelhq-wvag.4`) — code complete and reviewed, NOT pushed. Issue CLOSED.**

Branch `isr-herd/04-streams-publisher`, rooted on PR 3. Serves decisions 3, 8 and 13. Much the
largest PR in the stack: it introduces the repo's **first** DynamoDB stream, first event source
mapping, first SQS queue, first CloudWatch alarm, and a second account-level Lambda.

Built as **four sequential units**, each independently testable, because the issue bundles a
schema change, a new DO class, a new AWS Lambda, an observability stack and a credential
removal into one ticket:

1. **The snapshot Durable Object** (`workers/isr-writer/src/{snapshot,isr-snapshot,build,r2}.ts`)
   — one coordinator per build at `idFromName(isrPrefix)`, which is what makes the in-memory
   merge safe and the CAS loop unnecessary. New op `POST /<isrPrefix>/tags` on the existing
   per-deploy write secret. Plus the Go generalization that lets a script gain a DO class.
2. **The edge stops writing R2** and raises through the DO instead, over a new `ISR_WRITER`
   service binding.
3. **DynamoDB Streams + the account-level publisher Lambda** (`packages/tag-publisher/`),
   mirroring `cloud/aws/bootstrap/optimizer.go` end to end, with the ESM, a DLQ and an alarm.
4. **The Lambda publisher and the last standing R2 credential are gone.**

### The credential is finally gone — and this time it is demonstrated, not asserted

A deployed function's entire env surface is now `OCEL_ISR_STORE_BUCKET`, `OCEL_ISR_WRITER_URL`,
`OCEL_ISR_WRITER_SECRET`, `OCEL_HANDLER`. No access key, no secret, no SSM parameter carrying
one. `cloud/aws/deploy/function.go` has no `ssm:GetParameter` and no `kms:Decrypt` left (the one
remaining `kms:Decrypt` in `cloud/aws/deploy/` is the variable store's own key, unrelated).
`cloud/aws/cmd/lambdanode/bootstrap/config.go` was deleted outright. Regression-tested by
`TestISRCacheStore_LeavesNoStandingCredentialOnTheFunction`, which renders the full env and the
full policy and asserts the absences.

The only R2 credential left on the substrate is the deploy host's, held by a short-lived
provider process. `cacheStorePermissionGroup` **cannot** be narrowed — the deploy host needs
read+write+list+delete bucket-wide for assets, edge bundles, prerender seeding, genesis, prune,
preview teardown and project destroy, and R2 has no key-prefix grammar. That is now documented
where the comment used to promise a narrowing.

### Three decisions were taken before building; they are recorded on the issue

The Lambda **stops raising snapshots entirely** (its DynamoDB write is the raise); the
**heartbeat ships but the staleness alarm is deferred** to `ocelhq-wvag.13`; and **deploy
genesis seeds both stores**, because `deployedAt` has exactly one writer and an unanchored
snapshot never prunes. `bd show ocelhq-wvag.4` carries the full text plus four corrections to
the issue's own scope — including that its proposed ESM filter was **unsafe**: upload-session
items share `sk = "#META"` and carry HMAC secrets, so the filter must also constrain `pk` to
`TAG#`. It ships constrained, and the consumer re-derives the build from `gsi1pk` in code as an
independent second defence.

### What the review turned up

Two independent reviews (spec + adversarial). Six findings, all fixed:

- **The raise path created zero-anchored snapshots.** `deployedAt` has one legitimate writer,
  and the DO's `etagDoesNotMatch: "*"` stopped it *clobbering* an anchor but not *being* the
  creator. The same file already refused to create on the heartbeat path, for exactly this
  reason — the invariant was honoured on one path and violated on the other. Both publishers now
  decline. Verified first that an absent replica fails **closed**: `expired()` returns
  `"untrusted"` and `interception.ts` falls open to the origin, never serving stale as fresh.
- **One poison build dropped its batch-mates' invalidations.** No `ReportBatchItemFailures`, so
  a build stuck on a 401 took ~2 healthy builds to the DLQ with it every batch, silently.
- **A substrate with no adopted ISR writer DLQ'd every batch forever** and lit the alarm from
  the moment of bootstrap — the publisher rendered unconditionally while its seed did not.
- **The heartbeat never started for a quiet build** — it armed only on first raise, so builds
  most likely to be silently broken were exactly the ones `.13` would never see.
- An operator-facing message claimed an unpinned publisher falls back to the Lambda publisher
  **this same PR deleted**.
- Two concurrent bootstraps failed instead of converging on one seed.

Verified: `workers/isr-writer` 70; `packages/tag-publisher` 15; `packages/next-cache` 41;
`workers/nextjs` 529; `workers/deployments-store` 63; `packages/lambda-entrypoints` 190 of 191
(the known pre-existing `test/tag-clock.test.mts` case, which survives the publisher's retirement
because it covers cursor advancement, not publishing); `pnpm -r --no-bail typecheck` clean except
the pre-existing `examples/*`; `cloud/aws` and `cloud/edge` build and test clean.

### Two things PR 4 changed that deserve a human eye

- **The ISR write-secret seed became persistent account state.** It was minted per deploy run and
  never persisted, so nothing could reproduce a build's secret — and the account-level publisher
  must derive any build's secret. It is now `/ocel/edge/isr-writer-seed[-preview]`, create-only,
  SecureString, class-separated (a preview publisher provably cannot read production's, enforced
  by both the IAM resource list and the KMS encryption-context condition). But it weakens epic
  decision 6's "per-deploy rotating secret" to per-*build*: redeploying the same buildId no longer
  rotates, and compromise of that one parameter is forgeable write access to every build's ISR
  entries and tag clocks, with no rotation automation. This was the only route that avoided a
  second credential path; it is still a real change to a security property.
- **Two `tag-clock.json` copies now exist per build** — the R2 one the edge reads, written by the
  DO, and a new S3 one written by the publisher that **nothing reads until PR 5**. Both get the
  same genesis anchor and merge monotonically, so they converge independently. But nothing
  compares them, so until PR 5 a divergence in the S3 copy is invisible.

**PR 3 (`ocelhq-wvag.3`) — code complete and reviewed, NOT pushed. Issue CLOSED.**

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

**Nothing in the stack is waiting on scrutiny.** PRs 2, 3, 4, 5 and 7 are all reviewed and their
findings are fixed. Every remaining child is **blocked on a human**, so there is no unblocked
implementation work left in this epic:

- `ocelhq-wvag.6` — blocked on `.13` and `.14`, both human gates.
- `ocelhq-wvag.8` — blocked on `.7` (now closed) and `.9`, which **needs a deploy**.
- `ocelhq-wvag.9` — the one thing that unlocks the rest, and it needs live measurement on a
  zone route. It is the highest-value next action if a deploy can be authorized.

Two pieces of work remain that need no gate: **`ocelhq-wvag.12`** (collapse the twice-derived
projection key and filename into `@ocel/next-cache`; needs a dist build on that package, so it
touches Lambda and worker bundling — best done between PRs) and **`ocelhq-wvag.11`** (destroy
leaves per-build writer DO instances behind).

Five review rounds have now landed twelve real defects rather than polish, and **in every
single case the failure was silent**: a write dropped with no log, a herd at the auth boundary,
storage written for a caller who never authenticated, a projection that would miss forever if
two derivations drifted, a snapshot that grows unboundedly because it was created without an
anchor, one poison build quietly taking its batch-mates' invalidations to a DLQ, an alarm lit
permanently from the moment of bootstrap, a route that stopped refreshing colo-wide because its
admission record was only ever released on a throw and nothing on that path throws. Not one of
them threw. Keep weighting reviews toward *what fails without saying so* — that is where this
stack's bugs actually live.

A third method earned its keep on PR 7: **make the review clear the hard things explicitly
before it hunts**. The spec brief named the four properties most likely to be quietly wrong
(the join key's injectivity, the promise-ordering the `clone()` depends on, the shared in-flight
map's key spaces, every `catch`'s direction) and asked for proof or a counterexample on each.
It proved all four and then found the defect elsewhere — but a clean review that has *stated
what it checked* is worth far more than one that merely reports nothing.

Two methods have earned their keep and are worth repeating:

- **Reconnaissance before briefing.** PR 4 was scoped by a read-only pass over the real code
  before a line was written. It found the issue's own ESM filter to be unsafe, its line
  references stale, a migration flag that could not express the change being asked for, and an
  unanchored-snapshot problem the spec never mentioned. Three of those would have shipped.
- **Decomposing a large issue into units with fixed contracts.** PR 4 went out as four sequential
  briefs, each fixing a contract the next coded against. The one place a brief was wrong — it
  told Unit 4 that `publishTagSnapshot` was dead code — the implementer refused with evidence
  (Unit 3's consumer still needs CAS for the S3 copy, which has two sanctioned ESM readers) rather
  than deleting a live dependency. Brief them to push back.

Standing constraints PR 5 inherited and every later reader of a snapshot inherits too:

- **The remote tier stays fail-closed until first sync**, and an absent or unreadable snapshot
  is not a sync. That is deliberate, not an oversight to tidy up, and it must never be traded
  for a growth or latency win. PR 4's review confirmed the edge side end to end: `expired()`
  returns `"untrusted"` and `interception.ts` falls open to the origin.
- **Two `tag-clock.json` copies per build still exist** — the R2 one the edge reads and the S3
  one the origin now reads. They converge independently and nothing compares them.

Standing constraints PR 7 hands to PR 8, which builds L2 on top of L1:

- **Admission is route-scoped; storage and the miss-path join are variant-scoped.** L2 keys on
  `buildId:routePath` like L1 does. Do not extend route-scoped keying to anything whose response
  is *served* to a request — a joiner is answered with the leader's bytes, so an `.rsc` request
  joined to an HTML fill is answered with the wrong shape.
- **Every layer admits on error.** L1 fails open on an inert cache, a missing `delete`, and a
  `match`/`put`/`delete` that throws. Decision 10 already requires the same of L2.
- **L1's sizing is measured, not assumed.** `refreshSentinelTtlSeconds = 5` in
  `workers/nextjs/src/cache.ts` rests on the spike's ~200 ms cross-isolate visibility and its
  exact 1–60 s TTLs. `ocelhq-wvag.9` measures the write-visibility window that sizes L2, and
  that number is what tells you how much L1 leaks into L2 — do not size L2 off the epic's
  original `1 − crossIsolateHitRate` formula, which degenerates to zero.

Live threads to carry forward:

- `ocelhq-wvag.9` blocks `.8`. It needs a deploy, like `.1` did. With `.7` closed it is now the
  **only** thing standing between this stack and its last PR, so it is the next action.
- `ocelhq-wvag.13` and `ocelhq-wvag.14` both block `.6`. See the stack shape above.
- `ocelhq-wvag.12` is best done between PRs — it needs a dist build on `@ocel/next-cache`, which
  touches Lambda and worker bundling.

## The decisions waiting on a human

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

**PR 4 added a second, sharper reason to run it.** The Cloudflare migration generalization
assumes the script-settings endpoint reports a migration tag for a script migrated with the
*older single-tag form* — the form PR 2 shipped. If it does not, bootstrap hard-fails on exactly
the already-bootstrapped accounts the generalization exists to migrate. The pure logic is sound
and tested both ways; the API response shape is not something a unit test can reach. `bd show
ocelhq-wvag.10` carries a three-step reproduction, and **only step 3 actually tests it** — steps
1 and 2 pass either way.

### `ocelhq-wvag.14` — cut and pin the tag-publisher release artifact

Also a human gate, mirroring `ocelhq-pf6q.13` for the image optimizer. Both pin constants ship
empty, so bootstrap deliberately renders **no** publisher at all rather than failing. Until it is
cut, no origin-raised invalidation reaches a build's edge replica — only edge-raised ones. The
origin's own tag state is unaffected, so this is bounded, and it is why `.14` blocks `.6`.

### The ISR write-secret seed's new lifecycle

Not a gate, but it should be looked at rather than discovered later. PR 4 made the seed
persistent account state because the account-level publisher must derive any build's secret and
the alternative was a second credential path. The consequence is that the per-deploy rotation
epic decision 6 describes is now per-*build*: redeploying the same buildId does not rotate, and
that one SSM parameter is forgeable write access to every build's ISR entries and tag clocks,
with no rotation automation. Production and preview are provably separated. See PR 4's entry in
"Current position".
