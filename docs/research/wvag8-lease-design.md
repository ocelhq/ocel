# `ocelhq-wvag.8` — Edge L2 cross-colo revalidation lease Durable Object

Design + grilling. No code written. Branch `isr-herd/10-admission-jitter`, tip `6eddd0e`.

Serves epic decision 10, under the auth boundary decision 6c already drew for it
("snapshot and lease DOs stay unauthenticated behind the worker boundary").

---

## 0. What is already settled, and is not re-litigated here

- **One DO per route, `idFromName` on the route.** Sharding is rejected on merit (k shards ⇒ k
  renders); a shared coordinator is Cloudflare's named anti-pattern (correction 6).
- **The jitter is built and measured.** `admissionJitterMs = 1000` in `workers/nextjs/src/cache.ts`,
  `E = 1.41–1.46` claims per colo per stale event. `.16` is closed; no hierarchical
  per-`(route,colo)` tier is needed (`E` had not plateaued at `J = 2000`).
- **L1 is live and its key scope is proven.** All four synthetic-hostname tiers store in
  `caches.default` (spike §0 of the `.9` follow-up).
- **`waitUntil`-only, fail open on everything.** Non-negotiable; the acceptance criterion is
  written around it.
- **The draw and every deferral are capped by the entry's remaining stale window.** This is `.16`'s
  review finding and it generalises to L2 — §6.

---

## 1. Where the class lives, and how the edge reaches it

**`workers/isr-writer`, as a third DO class `IsrLease`.** Not a new package: decision 7's reason for
an account-level worker (the namespace must survive project redeploys) already holds, the generic
worker already carries the `ISR_WRITER` service binding (`genericISRWriterBinding`,
`cloud/edge/cloudflare/rootstack.go:138`), the auth boundary and the per-isolate secret memo already
exist there, and `pendingMigrations` already knows how to add a class to a live script.

**Reached over the existing service binding, never as a DO stub.** A DO namespace binding is
script-scoped; binding `IsrLease` into the generic worker with `script_name` would put an
unauthenticated namespace on the request-serving script, which decision 6c forbids. So the edge
calls `env.ISR_WRITER.fetch(...)` and `workers/isr-writer/src/index.ts` derives the object name
from the **authenticated** prefix — the same rule `entryObjectKey(isrPrefix, key)` follows today.

Cost: one extra worker-to-worker hop per consult. It is inside `waitUntil`, off the serving path,
and it is what buys the auth boundary and one provisioning site instead of two.

---

## 2. Wire protocol

Two ops, single path segments so `deployPrefix()`'s four-segment grammar is untouched
(`workers/isr-writer/src/index.ts:20-27`). Both authenticate with the deploy's own write secret via
the existing `authorized()` (per-isolate hash memo, no DO hop).

```
POST /<isrPrefix>/lease
  authorization: Bearer <deploy write secret>
  { "key": "<buildId>:<routePath>", "holdMs": <number>, "renderBudgetMs": <number> }
  → 200 { "grant": true }
  → 200 { "grant": false, "refill": false }   // a render is in flight elsewhere
  → 200 { "grant": false, "refill": true }    // a render landed; fresher bytes are below
  → 400 malformed key/body · 401 bad or absent bearer

POST /<isrPrefix>/lease-settle
  { "key": "...", "landed": true|false, "holdMs": <number> }
  → 204
```

`key` is exactly `CacheTarget.refreshKey` — `${buildId}:${routePath}`, built once in
`workers/nextjs/src/index.ts:769` and already the unit of admission at all three sites. The object
is `env.ISR_LEASE_DO.idFromName(`${isrPrefix}:${key}`)`. The `buildId` appears twice (it is the last
segment of `isrPrefix`); that is deliberate — the name is *the authenticated prefix plus the
caller's admission key*, and re-deriving the edge's admission key at the writer would be a second
spelling of it that could drift. A new build changes `isrPrefix`, so **a new build gets a fresh
namespace of names** for free, twice over.

`key` is validated at the boundary (length ≤ 512, no control characters) before it reaches
`idFromName`, for the same reason `entryObjectKey` validates: an unvalidated caller-supplied name
is an unbounded namespace.

### The DO

```ts
export class IsrLease extends DurableObject<Env> {
  private inFlightUntil = 0;   // a render is out; do not grant, and nothing new is below yet
  private suppressUntil = 0;   // a render landed at/after this-hold's start; refill instead

  claim(holdMs, renderBudgetMs): { grant: boolean; refill: boolean }
  settle(landed, holdMs): void
}
```

- `claim`: `now < inFlightUntil` → `{grant:false, refill:false}`. Else `now < suppressUntil` →
  `{grant:false, refill:true}`. Else `inFlightUntil = now + renderBudgetMs; return {grant:true}`.
- `settle`: `inFlightUntil = 0; suppressUntil = landed ? now + holdMs : 0`.

**No storage, no alarms, no `await` anywhere in either method.** Three consequences, all
load-bearing:

1. It is a "simple operation" in the sense Cloudflare's 500–1 000 rps rating uses — the epic's
   doubt about "a lease DO persisting state per claim" is answered by not persisting.
2. `claim` performs no `await` between reading and mutating state, so it is atomic without input
   gates. (A storage `await` in the middle would open an interleaving window in which two callers
   both read `inFlightUntil = 0` — the whole point of the object, lost silently.)
3. Losing the state loses **suppression**, never correctness: an evicted or hibernated object grants
   the next caller, which is today's behaviour. Lazy creation and hibernation are therefore free,
   and `ocelhq-wvag.11` (destroy leaves DO instances behind) gains nothing to prune — an `IsrLease`
   that has hibernated holds no bytes and no alarm.

`renderBudgetMs = 30_000`: a grant that is never settled (isolate torn down mid-render) must not
suppress the route forever, and a caller inside `waitUntil` cannot still be rendering after the
Workers post-response budget has lapsed. `holdMs`: see §4.

---

## 3. Where the consult sits in `cache.ts`

Inside `admitRefresh`, **after** `refreshOnce` (L0) and **after** `claimSentinel` (L1) returns true,
before `run()`:

```
admitRefresh(deps, key, run, staleForMs, refill?)
  refreshOnce(deps, key, async () => {          // L0 — one per isolate, held across the wait
    await admissionDelay(staleForMs)            // the jitter, .16
    if (!(await claimSentinel(...))) return     // L1 — one per colo per TTL
    const verdict = await consultLease(...)     // L2 — NEW
    if (!verdict.grant) {
      if (verdict.refill) await refill?.()
      await settleSentinel(cache, sentinel, /* landed */ verdict.refill)
      return
    }
    let landed = false
    try { landed = await run() }
    finally {
      await settleSentinel(cache, sentinel, landed)
      await deps.lease?.settle(key, landed, holdMs)   // never only on throw
    }
  })
```

**That ordering is the whole sizing argument and it must be asserted, not assumed.** L2 traffic is
`C·E` precisely because only callers that already survived L0 *and* L1 *and* the jitter reach it.
Moving the consult ahead of `claimSentinel` would multiply DO traffic by the 39–42× the jitter
removed and no functional test would notice. §8 names the test.

**All three admission sites get it, because all three go through `admitRefresh`** —
`cache.ts:624` (colo-tier stale), `index.ts:820` (PPR stale), `index.ts:856` (R2-complete stale).
Only the first supplies a `refill` thunk; the other two do not need one:

- **PPR (site 2)** composes its shell from a per-request R2 read. Once the winner rewrites R2 the
  next request sees fresh bytes with no refresh at all.
- **R2-complete (site 3)** is reached through `cachingOrigin`, i.e. on a colo miss/expiry, and the
  miss path has already memoized what R2 held. Its next request takes site 1's path.
- **Colo-tier stale (site 1)** is the one with no self-healing path: `serveOrAdmitRefresh` serves
  the colo entry and never consults the tier below, so absent a refill a denied colo keeps serving
  stale until *something* rewrites its entry. §4.

`refill` for site 1 is `store(keyRequest, target, deps, policy, await origin())` — the same
non-blocking `origin` thunk `colo()` already holds and hands the miss path. With interception wired
that is an R2 read and **zero** origin invocations; without it, it is a plain (non-`x-prerender-revalidate`)
Lambda call that returns the Lambda's own cached bytes rather than a render. Acceptance is therefore
stated in **origin renders**, and `.17` measures renders.

### Deps and wiring

`CacheDeps` gains `lease?: LeaseClient` (`{ claim(key, holdMs, staleForMs): Promise<Verdict>;
settle(key, landed, holdMs): Promise<void> }`). It is built in `resolveRouteDeps`, which is the only
place that holds all three of `env.ISR_WRITER`, `record.isrPrefix` and `record.isrWriteSecret` —
the last of which today rides only into the edge bundle's `CacheEntrypoint` props
(`index.ts:364`). `cache: base.cache && { ...base.cache, lease }`.

Spreading `CacheDeps` is safe and this is not incidental: `inFlight` is a `WeakMap` keyed on
`deps.cache` (the `Cache` object), not on the deps object (`cache.ts:419`), so adding a field does
not fragment L0. The handoff flags this as load-bearing for `.8`'s wiring and there is a test on it.

The image tier shares `CacheDeps` and never sets `refreshKey`, so it never reaches `admitRefresh`
and the lease is inert there. Correct: an image is invalidated by its content hash, not by a stale
route.

---

## 4. Suppression: what a denial means, and why it must expire

The refill is what makes the acceptance criterion reachable. Without it, denial only *serialises*
the herd — colo B is denied, keeps serving stale, re-admits every `refreshSentinelTtlSeconds`, and
eventually renders for itself once the hold lapses: ~`C` origin renders per stale event, spread out
instead of bursty. With it, colo B pulls the winner's bytes out of R2, its colo entry becomes fresh,
and it stops admitting entirely: **one origin render per stale event**.

The suppression must expire, and the expiry is the single most dangerous constant in this design:

- **No expiry (rely on DO eviction) deadlocks.** The next stale event's first caller would be told
  "refill", refill stale-again bytes, stay stale, and re-ask forever — nobody renders. Eviction
  timing is an implementation detail, not a contract, so a design that is correct only because
  objects happen to evict is a design whose central guarantee is untested.
- **A timestamp comparison (`caller's entry lastModified` vs `landedAt`) deadlocks the same way.**
  A refilled entry's `lastModified` is the *render's* time, which is always earlier than the DO's
  settle time, so `modifiedAt < landedAt` holds forever and every later stale event is answered
  "refill". Rejected. (It also mixes an AWS clock with a Cloudflare one.)
- **A fixed hold breaks short-`revalidate` routes.** A 30 s hold on a `revalidate: 5` route caps
  cross-colo re-rendering at once per 30 s — a freshness regression L2 has no licence to introduce.

**Settled: `holdMs = clamp(revalidateMs, 5_000, 60_000)`, clamped again by the entry's remaining
stale window (§6).** `revalidateMs` comes from the same `EntryMeta` the caller already computed, so
the hold is "one render per stale event" by construction; the 5 s floor keeps it at or above the L1
sentinel TTL so a refill loop can complete inside one hold; the 60 s ceiling bounds how long an
abandoned-but-settled lease can defer the next event. The policy lives in `cache.ts`, where the
window is known; the DO only stores the deadline it is handed. (Open decision 2.)

The refill loop terminates by construction: a refill that reads fresh bytes makes the colo entry
fresh, and a fresh entry never reaches `admitRefresh` again. A refill that reads bytes the winner
has not yet replaced leaves the entry stale, and the colo re-asks one sentinel TTL later — an R2
read per colo per 5 s, self-terminating.

---

## 5. Fail open

**Every uncertainty resolves toward `grant`** — i.e. toward exactly today's behaviour, which is a
render. Concretely, `consultLease` returns `{grant:true, refill:false}` when any of these happens:

| failure | why it is a grant |
| --- | --- |
| `deps.lease` absent (no binding, no prefix, no secret) | a substrate without a writer must serve, not stall |
| `fetch` rejects (binding down, worker not deployed) | unreachable coordinator ≠ authority to stop refreshing |
| non-200 (401 after a secret rotation, 429, 5xx, 404) | the writer's own overload path |
| body is not JSON, or `grant` is not a boolean | a shape change must degrade, not decide |
| the consult exceeds `leaseTimeoutMs` | overload — see below |

**`leaseTimeoutMs = 1_000`**, applied as `AbortSignal.timeout(min(leaseTimeoutMs, remainingStaleMs))`
on the service-binding fetch. One second is the regime the design is sized for: the burst is defined
as `C·E` requests spread over exactly `J = 1 s`, so at 423–438 rps against a ≥500 rps object the
queue formed by one stale event drains inside that second. Waiting longer buys an answer that is
worth less than the render it is delaying — the same argument the origin's entry read uses for
3 s against the write's 10 s (`packages/lambda-entrypoints/src/next/isr-writer.mts:19-25`).

`lease-settle` gets the same treatment and its failure is swallowed entirely: an unsettled lease
lapses at `renderBudgetMs`.

**Under genuine DO overload the design degrades to precisely the pre-`.8` system**: every consult
times out, every caller fails open, `C·E ≈ 430` colos render. That is the "fail open on overload"
decision 10 requires, and it is why the 1.2× margin (§7) is survivable rather than fatal.

**Fail-open is logged, denial is not.** `console.warn` once per fail-open, with the reason. Denials
run at hundreds per second and would drown the signal; fail-opens should be rare, and if they are
not, the log volume is itself the alarm. (Observability is already enabled on the writer script.)

---

## 6. The boundary where a herd could come back

`.16`'s review found that the jitter reintroduced the herd at exactly the boundary it pushed the
refresh across: a route whose stale window is shorter than `J` spent the tail of the wait past
`expiration`, where the colo tier declines to serve, the request falls to the miss path, and the
only dedupe left is the per-isolate `refreshOnce`. L2 can do the same thing two more ways, and both
get the same fix:

1. **The consult itself is a deferral.** A queued consult can carry the refresh past `expiration`.
   Fixed by `min(leaseTimeoutMs, remainingStaleMs)` above, where `remainingStaleMs` is
   `staleForMs` **minus the jitter already slept** — the two deferrals are cumulative and must be
   budgeted together, not each against the original window.
2. **A denial is a deferral of unbounded length.** Suppressing a refresh past the entry's
   expiration converts a stale-serving colo into a colo that misses on every request — on a
   substrate with interception that is an R2 read per isolate (cheap, coalesced), but without it
   that is a Lambda call per isolate. Fixed by `holdMs ≤ remainingStaleMs`: **L2 may never defer or
   suppress a refresh past the point where the entry stops being servable.** On the pathological
   route this degrades L2 to a no-op, which is un-jittered pre-`.8` behaviour — never worse.

Stated as one invariant, because three separate constants enforcing it is how one of them gets
missed: *the jitter draw, the consult timeout and the suppression hold are all bounded by the same
`staleForMs` the entry declared, and the first two are bounded by what is left of it.*

---

## 7. Acceptance criterion: the C at which one object saturates

Measured inputs (spike §10–§11, two independent runs of 100 trials, `N = 128`, `J = 1000 ms`, zero
discarded):

```
E = 1.41 – 1.46            escapes (L1 claims) per colo per stale event
C ≈ 300                    colos — AN ASSUMPTION, not a measurement (Cloudflare cites 330+ cities,
                           and colos per city are not one)
```

Every escape is exactly one `claim` call, so the L2 arrival rate is the burst rate:

```
F = C · E = 300 × 1.41 … 300 × 1.46 = 423 … 438 requests per stale event,
    spread over exactly J = 1 s  ⇒  423–438 rps, instantaneous, at ONE object.
```

Against Cloudflare's "approximately 500–1 000 requests per second for simple operations":

```
margin (conservative end):  500 / 438 = 1.14×   …   500 / 423 = 1.18×
saturation, conservative :  C = 500 / 1.46 = 342   …   500 / 1.41 = 355   ⇒  C ≈ 342–355
saturation, optimistic   :  C = 1000 / 1.46 = 685  …  1000 / 1.41 = 709   ⇒  C ≈ 685–709
```

**One per-route object saturates the conservative rating at `C ≈ 342–355` colos, and the optimistic
one at `C ≈ 685–709`.** At the assumed `C ≈ 300` the margin is ~1.2×, not the ~6× the 85–88 rps
sustained figure sitting beside it suggests. `R = C·E / refreshSentinelTtlSeconds ≈ 85–88 rps` is
the sustained rate and is **not** the constraint.

Two terms that could be read as additive to `F` and are not:

- **Settles are ~1 per stale event** (only a grantee settles), so they are noise beside `F`.
- **Re-poll waves do not superpose.** A colo whose refill did not find fresh bytes re-admits one
  sentinel TTL later and draws a *fresh* jitter delay, so the DO sees a second ~430 rps second at
  `t ≈ 5–6 s`, not a rising sum. Peak stays `F`.

What this buys, stated so it can be checked: the pre-jitter figure was `F ≈ 20 000–23 000` requests
in a few hundred ms (~40× the ceiling, ~20 s of queueing). The jitter takes it to just inside the
rating; L2 is being built at a 1.2× margin and is required to fail open because of it. A lease
built as though it had 6× of headroom would be built wrong.

**The lever if `C` turns out to be >340**, recorded rather than pre-emptively pulled: raising `J` to
2 000 ms drops `E` to 1.21–1.26 (measured, same sweep, and `E` had not plateaued), moving saturation
to `C ≈ 397–413` conservative / `794–826` optimistic, at the cost of up to a second more staleness
before a refresh starts. It is a constant, not a redesign. (Open decision 3.)

---

## 8. Go provisioning

`.4` established the pattern and it is followed literally. Three literals and one type:

1. `workers/isr-writer/wrangler.jsonc`: a `durable_objects.bindings` entry
   `{name: "ISR_LEASE_DO", class_name: "IsrLease"}` and a **new, appended, never-edited** migration
   step `{tag: "v3", new_sqlite_classes: ["IsrLease"]}`.
2. `workers/isr-writer/src/env.ts`: `ISR_LEASE_DO: DurableObjectNamespace<IsrLease>`; the class is
   re-exported from `src/index.ts` beside `IsrDeploy` and `IsrSnapshot`.
3. `cloud/edge/cloudflare/rootstack.go`, `isrWriterWorker`: one more `durableObjectClass`
   `{binding: "ISR_LEASE_DO", className: "IsrLease"}` and one more `migrationStep`
   `{tag: "v3", sqliteClasses: []string{"IsrLease"}}`.

Nothing else in Go changes. `putDurableObjectScript` binds every class in `do.classes`, and
`pendingMigrations` keys on the classes the **deployed** script reports (the migration tag is
unreadable from the API — see `durableobjectmigration_test.go`'s pinned live fixture), so an account
already carrying `IsrDeploy` + `IsrSnapshot` gets exactly one step, `new_sqlite_classes:
["IsrLease"]`, under `new_tag: "v3"`, and a fresh bootstrap gets all three. Redeclaring an existing
class is what Cloudflare rejects with 400/10074, and the existing logic is what prevents it.

**No change to the generic worker's provisioning**: `ISR_WRITER` is already bound
(`genericISRWriterBinding`). The only edge-side change is that `resolveRouteDeps` starts using
`record.isrWriteSecret` on the serving path as well as in the edge bundle's props.

---

## 9. Test plan

Every assertion marked **[M]** is mutation-checked: the production line is broken, the named test
confirmed failing, the line restored. That is the standing bar in this stack and the reason five
review rounds' worth of dead detectors were found.

### `workers/nextjs` (unit, vitest, all deps built through `test/cache-deps.ts`)

Ordering and sizing — the assertions the whole 423–438 rps figure rests on:
- **[M]** No lease call at all when `claimSentinel` finds the colo already holds the sentinel
  (reuse `dispatch.test.ts`'s `coloHoldingSentinel`). Mutation: move the consult above the claim.
- **[M]** No lease call when `refreshOnce` already holds the key in this isolate.
- **[M]** The consult happens after the jitter, not before: injected `admissionDelay` records the
  order.

Verdict handling:
- **[M]** `grant:false, refill:false` ⇒ `originBlocking` is never called and the refill thunk is
  never called.
- **[M]** `grant:false, refill:true` ⇒ the refill thunk runs exactly once, its response is stored in
  colo, and `originBlocking` is never called.
- **[M]** `grant:true` ⇒ render runs, then `settle(landed: true)`; an origin `!response.ok` settles
  `landed: false`; a **thrown** render still settles (the settle is in `finally`, initialised
  `false`). This is the exact shape of the "claim released only on a throw" defect this stack has
  already shipped once.
- **[M]** Sites 2 and 3 pass no refill thunk and a `refill:true` verdict there is a no-op.

Fail open — **exercised, never asserted**. One test per row of §5's table, each driving the real
`consultLease` over a stubbed binding: rejecting fetch, 500, 401, 429, `text/html` body, `{"grant":
"yes"}`, and a fetch that never resolves (fake timers past `leaseTimeoutMs`). Each asserts the
origin render *happened* and the served response was unaffected. **[M]** on the `catch` (rethrow)
and on the timeout (remove the signal).
- **[M]** `deps.lease === undefined` reproduces pre-`.8` behaviour exactly.

Boundaries (§6):
- **[M]** The consult's deadline is `min(leaseTimeoutMs, staleForMs − jitterAlreadySlept)`, asserted
  on an exported pure function (`leaseDeadlineMs`), the way `admissionDrawMs` is asserted directly
  rather than through a timer.
- **[M]** `holdMs` is clamped by `staleForMs`; on a `cacheLife({revalidate:1, expire:2})` route L2
  degrades to a no-op rather than suppressing past expiration.

Serving path:
- **[M]** The served response resolves while the lease promise is still pending (`waitUntil`-only).
  Mutation: `await` the consult in `serveOrAdmitRefresh`.

Wiring — the "seam left unwired" guard, which is the failure this stack keeps producing:
- **[M]** `resolveRouteDeps` produces a `lease` when binding + `isrPrefix` + `isrWriteSecret` are all
  present, and `undefined` when any one is missing. Without this test, a lease that is never
  constructed in production passes every other test in this file.
- **[M]** Adding `lease` to `CacheDeps` does not fragment L0 (`inFlight` is keyed on `deps.cache`).

### `workers/isr-writer`

- `test/lease.test.ts`, the class over a fake `ctx`: grant → deny(in-flight) → settle(landed) →
  deny(refill) → past `holdMs` → grant. **[M]** on each transition.
- **[M]** `settle(landed:false)` grants immediately (a failed render must not suppress its retry).
- **[M]** `inFlightUntil` lapse grants after `renderBudgetMs` with no settle at all.
- **[M]** Two `claim`s awaited concurrently yield exactly one grant (the atomicity `claim` gets from
  having no `await`; the mutation is to insert one).
- `test/index.test.ts`: 401 without/with a wrong bearer on both ops; 400 on a malformed key or body;
  404 on the wrong method; and **[M]** the object is named `${isrPrefix}:${key}` — asserted through a
  namespace stub that records the `idFromName` argument, mutation being to name it from the
  caller-supplied key alone (which would let one deploy address another's lease).

### Go

- `pendingMigrations` against `["IsrDeploy","IsrSnapshot"]` yields exactly one step,
  `["IsrLease"]`, tagged `v3`; against `[]` yields all three steps; against a class not in the log
  still errors. **[M]**
- `buildDurableObjectScriptMultipart` emits the `ISR_LEASE_DO` binding.

### e2e

Two things unit tests structurally cannot reach, both worth a single live run on the existing
substrate (account `363236815301`):

1. **The v3 migration applied to a script that already carries v1 and v2.** 400/10074 is a real,
   already-hit failure mode in this repo, and it only exists against a live script.
2. **One round trip through the deployed writer**: seed a deploy secret via `initialize`, then
   `claim` → `claim` (denied, in-flight) → `lease-settle` → `claim` (denied, refill) and confirm the
   grant/deny shape and the 401 on a wrong bearer.

Not covered here and deliberately: the before/after collapse in origin invocations per stale event
is `ocelhq-wvag.17`'s acceptance, needs a multi-region driver, and is not this issue's.

---

## 10. Grilling — which guarantees could be false while every test stays green

1. **The consult could be wired ahead of L1 and nothing functional would break.** The lease would
   still grant and deny correctly; only the traffic would be 39–42× the sizing. This is the single
   most dangerous silent failure in the design, and it is why the ordering assertions in §9 come
   first and are mutation-checked. *An untested ordering is a sizing number with no detector.*
2. **`deps.lease` could be `undefined` in production and every cache test would pass**, because the
   seam is optional by design (fail open) — the same shape as `admissionDelay`, which the suite had
   to be restructured around. Mitigations: the `resolveRouteDeps` wiring test, and the fact that
   fail-open logs. If the log is silent *and* denials never happen, the tier is dead and invisible.
   The honest position: unit tests cannot prove L2 is live; `.17` is what proves it, and until `.17`
   runs, "L2 works" is a claim, not a measurement.
3. **`C ≈ 300` is an assumption wearing a measurement's clothes, and the burst rate is linear in
   it.** Nothing in this stack has measured `C`. Cloudflare publicly cites 330+ cities; at
   `C ≈ 342–355` the design is at the conservative ceiling. Stated as the acceptance criterion
   rather than hidden, with the `J = 2000` lever recorded.
4. **`E = 1.41–1.46` was measured in one colo, at `N = 128`, on a probe with no L0**, against a
   claim primitive shaped exactly like `claimSentinel` with `refreshSentinelTtlSeconds = 5`. The
   staleness clause runs both ways (spike §13): changing the claim's shape, the sentinel TTL, or
   where the delay is taken voids `E`, and voids this sizing with it. **Adding the L2 consult does
   not change the claim's shape** — it happens strictly after `claimSentinel` returns — which is the
   only reason `.16`'s numbers survive `.8` at all. Any future move of the consult re-opens the
   spike.
5. **`E` was also never measured against `N`**, only against `J`. `E(128)` is what we have; a colo
   with more isolates could escape more. `I_colo ≥ 99` has never plateaued, so `F` is a floor.
6. **Cloudflare's 500–1 000 rps is a rating, not a measurement of this object.** The design answers
   the "simple operations" caveat by persisting nothing, but no run in this stack has driven a real
   DO at 430 rps. The fail-open path is what makes being wrong here survivable, which is why §9
   exercises it seven ways instead of asserting it once.
7. **The timeout aborts the caller, not the object.** A timed-out `claim` may still be granted
   server-side. That is safe *only because* the timed-out caller fails open and renders anyway, so
   the grant is not orphaned — but it means grants can exceed renders under overload, and an
   `inFlightUntil` that outlives a caller that already rendered can suppress a *subsequent* legitimate
   refresh for up to `renderBudgetMs`. Bounded, capped by `staleForMs`, and it never breaks serving.
8. **The herd at the boundary, three ways** (§6): the jitter draw, the consult, and the suppression
   are all deferrals of the same refresh, they are cumulative, and any one of them crossing
   `expiration` drops the request to the miss path where only per-isolate dedupe remains. The fix is
   one invariant enforced at all three, and the `revalidate:1/expire:2` test is the one that fires.
9. **The suppression's expiry is where a deadlock hides.** Two natural-looking designs — rely on DO
   eviction, or compare the caller's entry timestamp to the landed time — both deadlock into "nobody
   ever renders again", and both would pass a test suite that only ever exercises one stale event.
   The test that catches it is the *second* stale event on the same object, and it is in §9.
10. **Refill hides a render.** If the refill silently fetches the same stale bytes (winner slow, R2
    not yet consistent), the colo stays stale and the system looks fine — content is served, no
    error, no log. The bound is the re-poll every `refreshSentinelTtlSeconds` and the `holdMs`
    expiry after which someone renders for real. Worst case is `holdMs` of extra staleness on a
    route whose winner died, never a broken serve.
11. **The clock the hold is measured on is the DO's, and the freshness it protects is the entry's.**
    They are deliberately not compared (§4) — the hold is a duration, never a comparison — which is
    what keeps AWS/Cloudflare skew out of the correctness argument entirely.

---

## 11. What this issue does not do

- Measure the before/after collapse (`ocelhq-wvag.17`).
- Measure `C`.
- Change L1, the jitter constant, the sentinel TTL, or the claim primitive — all four are pinned by
  the spike's two-way staleness clause.
- Add any state that needs pruning on destroy (`ocelhq-wvag.11` is unaffected).
