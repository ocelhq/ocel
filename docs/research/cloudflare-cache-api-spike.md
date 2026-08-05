# Cloudflare Cache API spike — cross-isolate visibility, honored TTL, isolate count

Status: **MEASURED** on 2026-08-04 against a zone-routed deploy at `probe.ocel.dev`, five
runs, all reaching colo `JNB`. Headline: the Cache API is **colo-shared, not isolate-local**
(the worst of five runs hit 2386 of 2388 cross-isolate reads; the other four were exact), and
`cache.put` TTLs are
**honored to the second with no floor** from 1s to 60s. **L1 is viable as designed.** The
deployment has since been torn down; the numbers below are what it reported.

Serves `ocelhq-wvag.1` / epic decision 12. Gates `ocelhq-wvag.7` (edge L1 sentinel) and
`ocelhq-wvag.8` (L2 lease Durable Object).

**Read the follow-up section too before sizing anything.** `ocelhq-wvag.9` measured the write-
visibility window this run left open and, in doing so, **corrects the "~200 ms" figure below by
a factor of twenty-five**: the window is 8 ms, and the 201 ms is this instrument's own round
trip plus its cold sockets and burst queueing — the window is not visible inside it at all.
Every claim below that reasons from "~200 ms" is superseded there.

## Why this exists

Two behaviours the L1 sentinel design rests on are undocumented by Cloudflare:

- **Cross-isolate visibility.** L1 is a colo-shared sentinel: one isolate writes it and every
  *other* isolate in the colo is supposed to see it and suppress its own revalidation. If the
  Cache API is instead isolate-local, L1 suppresses nothing and L2 must be sized for
  isolate-count fan-in rather than colo-count fan-in — a design change to child 8. A colo is
  many machines, so the answer is a **rate**, not a yes/no: the measurement that matters is
  what fraction of foreign-isolate reads see the write, because that fraction is the
  suppression factor L2 must be sized by.
- **Honored minimum TTL.** `workers/nextjs/src/tag-clock.ts` already ships
  `snapshotTtlSeconds = 10` and fronts the tag-snapshot read with a `cache.put` carrying
  `cache-control: max-age=10`. Cloudflare documents minimum edge TTLs only for zone-level
  Cache Rules; nothing documents a floor for `cache.put`. If `cache.put` floors TTLs upward,
  invalidation latency is longer than the tag-clock design assumes; if it evicts early, the
  front is doing less work than assumed.

A third number, **isolates per colo under load**, sizes L2 fan-in either way.

## The instrument

`workers/cache-probe` — a Worker plus a runner script. See that package's README for the
surface and the exact commands. Shape of the measurement:

- Each response names the isolate that served it (a module-scope id minted once per isolate)
  and `request.cf.colo`.
- `PUT /entry?run=&ttl=` does one `cache.put` of a sentinel naming the writing isolate, then
  immediately `cache.match`es it from that same isolate as a **positive control**: a run where
  nothing ever hits can then be told apart from a cache that never stored anything.
  `GET /entry?run=` does one `cache.match` and reports whether it hit, which isolate wrote
  what it found, and Cloudflare's own `age` and `cache-control` on the entry.
- All elapsed times are measured by the runner against its own clock, and stamped **per read**
  rather than per burst, so a slow read in a burst cannot inflate the propagation delay
  attributed to a fast one. No two Worker clocks are ever differenced: `Date.now()` inside a
  Worker advances only on I/O, so cross-isolate arithmetic on it would measure the runtime,
  not the cache.
- The sentinel result is a **rate**: cross-isolate hits over cross-isolate reads, in the colo
  the PUT itself reported. The verdict has a `partially-visible` tier between full sharing and
  `isolate-local` precisely because partial sharing is the expected shape of a real result.
- The TTL phase runs **after** the sentinel phase and uses its answer. If the cache is not
  shared, only a read that landed on the writer's isolate can observe the entry, so the runner
  records the serving isolate per read and the analysis counts only those polls. A window in
  which no poll could observe the entry yields `indeterminate`, never `evicted-early`.
- `age` and `cache-control` as reported by Cloudflare are carried into the TTL summary. They
  are measurement-independent: `age` bounds the entry's lifetime from Cloudflare's own clock
  regardless of polling luck, and a `cache-control` differing from what was written answers
  the floored-TTL question directly.
- Verdicts are computed by `src/analysis.ts`, which is unit-tested, and are deliberately
  conservative: a run that cannot distinguish two outcomes reports `inconclusive` or
  `indeterminate` rather than guessing.

### Limitations that survive a correct run

Record these alongside any numbers below; they bound what the numbers mean.

- **One runner reaches one colo, and a colo is many machines.** The census and the sentinel
  measure the machines this runner's requests happened to reach. A `partially-visible` result
  cannot be decomposed into "which machines" from outside.
- **Connection-to-isolate affinity is undocumented.** undici pools keep-alive sockets per
  origin, so the census opens a fresh dispatcher per round rather than replaying round 1's
  connections. Whether Cloudflare pins a request to an isolate by connection at all is exactly
  the layer being measured, so the isolate count is a **lower bound** on the colo's real
  isolate count, and L2 sizing in child 8 must treat it as such.
- **The TTL bracket is only as tight as `--pollSeconds`, and only as dense as the polls that
  reached the writer's isolate.** On an isolate-local cache, `authoritativePolls` will be far
  below `polls`, and the lifetime is bracketed loosely. `maxObservedAgeSeconds` is the
  evidence that does not depend on this at all.

### What has been validated, and what that is worth

Locally: `pnpm typecheck`, `pnpm build` (`wrangler deploy --dry-run`) and `pnpm test` all
pass, and the runner was driven end-to-end against `wrangler dev`. That run reported
`1 isolate`, sentinel `inconclusive` (`crossIsolateReads: 0`, so no rate exists to report),
and TTL `indeterminate` — exactly what a single-isolate, single-process local runtime should
report. Miniflare's positive control passes (`verified: true`) and it echoes back the written
`cache-control` unchanged, which is the wiring being checked, not an answer.

**Local passes prove the probe works. They prove nothing about the answers.** Miniflare's
cache is an in-process store in one isolate with no colo and no eviction policy resembling
Cloudflare's. The three questions above have no local approximation.

## Precondition that constrains where this can be measured — and where L1 can work at all

`caches.default` is **inert on `*.workers.dev`**: `put()` is accepted and silently discarded,
`match()` never hits. Cloudflare promises functional cache operations only for Workers served
from a zone.

That is not just a probe-deployment footnote. In `cloud/edge/cloudflare/cloudflare.go`,
`deployApp` enables the workers.dev subdomain only when the app declares no domains, and
otherwise attaches the worker to zone routes (`<host>/*`) and returns the canonical domain
URL. So:

- An Ocel deploy **with domains** is zone-routed, and the colo cache — the existing tag-clock
  front, the image-optimizer colo tier, and the proposed L1 — is live.
- An Ocel deploy **without domains** answers only on workers.dev, where all of that is a
  silent no-op. It is not a correctness bug (every layer is designed to fail open to a slower
  read), but it means L1's benefit is zero for domainless deploys, and any load test run
  against a workers.dev URL will measure the un-cached path no matter what this spike finds.

The probe must therefore be deployed on a zone route, and any later load harness
(`ocelhq-wvag.8`) must be too, or its numbers are about the wrong system.

## Results

Measured 2026-08-04. Every figure below is copied from the runner's own output; none is
estimated, rounded or interpolated.

### Run metadata

| field | value |
| --- | --- |
| date | 2026-08-04, 10:11–10:19 UTC |
| probe host (must not be workers.dev) | `probe.ocel.dev` (zone route `probe.ocel.dev/*`, zone `ocel.dev`) |
| runner location | one host outside Cloudflare; every request in every run landed in colo `JNB`, and `foreignColoObservations` was `0` in all five runs |
| raw run files | `workers/cache-probe/runs/run{1,2,3-ttl1,4,5-wide}.json` (gitignored, on the machine that ran them) |

The five runs and their invocations:

| run | invocation (all `node scripts/probe.ts --base https://probe.ocel.dev`) | started |
| --- | --- | --- |
| run1 | defaults | 10:11:43Z |
| run2 | `--ttls 1,5,10,30,60` | 10:12:33Z |
| run3 | `--ttls 1,2 --pollSeconds 0.25 --rounds 2` | 10:15:17Z |
| run4 | defaults | 10:15:46Z |
| run5 | `--concurrency 200 --rounds 6` | 10:16:24Z |

A deployment note that cost a run and is worth recording: `ocel.dev` also carries a wildcard
`*.ocel.dev/*` route belonging to another worker. For several minutes after `wrangler deploy`,
a fraction of requests (3 of 128 in one burst, ~25% immediately after deploy) were served by
the wildcard's script instead of the probe, which aborts the runner on a 404. It settled on
its own; all five runs above were taken after a burst of 200 came back clean. Any future
zone-routed probe on a zone with a wildcard worker route should verify with a wide burst
before trusting a run.

### 1. Cross-isolate sentinel visibility

| field | run1 | run2 | run3 | run4 | run5 |
| --- | --- | --- | --- | --- | --- |
| verdict | `cross-isolate-visible` | `cross-isolate-visible` | `cross-isolate-visible` | `cross-isolate-visible` | `cross-isolate-visible` |
| writer isolate / colo | `fb3f31e1` / JNB | `fb3f31e1` / JNB | `f649bf38` / JNB | `f649bf38` / JNB | `502374c7` / JNB |
| readable from the writing isolate (`verified`) | true | true | true | true | true |
| distinct reader isolates in the writer's colo | 28 | 33 | 28 | 35 | 99 |
| cross-isolate reads (denominator) | 412 | 456 | 199 | 409 | 2388 |
| cross-isolate hits | 412 | 456 | 199 | 409 | 2386 |
| **cross-isolate hit rate** | **412/412 = 1** | **456/456 = 1** | **199/199 = 1** | **409/409 = 1** | **2386/2388 = 0.9991624790619765** |
| writer-isolate reads / hits | 100 / 100 | 56 / 56 | 57 / 57 | 103 / 103 | 12 / 12 |
| first cross-isolate hit after the write | 204 ms | 206 ms | 201 ms | 203 ms | 251 ms |
| observations discarded as foreign-colo | 0 | 0 | 0 | 0 | 0 |

Run-to-run spread: the hit rate is `1` in four runs and `0.9991624790619765` in the fifth;
first cross-isolate hit spans 201–251 ms. The two misses in run5 are the only cross-isolate
misses in 3864 cross-isolate reads across all five runs, and they were **not** early: they
occurred at 3301 ms and 5115 ms after the write, on reader isolates `cd7d8345` and `215b4f6a`,
both reporting `writer: null`. So they are not propagation lag — they are two machines in JNB
that did not hold the entry, which is exactly the partial-sharing shape a colo-of-many-machines
predicts, just at a far lower rate than expected.

The hit rate is the load-bearing number, not the verdict. `2/500` and `500/500` are both
"some sharing" and mean completely different things for L2.

### 2. Honored TTL for `cache.put`

Poll bracket, `--pollSeconds 1` unless noted:

| requested TTL | run | verdict | last hit | first miss after it | still live at end of window | polls / authoritative polls |
| --- | --- | --- | --- | --- | --- | --- |
| 1s | run2 | `never-cached` | null | null | false | 2 / 2 |
| 1s | run3 (`--pollSeconds 0.25`) | `indeterminate` | 977 ms | 1300 ms | false | 5 / 5 |
| 2s | run3 (`--pollSeconds 0.25`) | `indeterminate` | 1940 ms | 2265 ms | false | 8 / 8 |
| 5s | run2 | `indeterminate` | 4289 ms | 5362 ms | false | 6 / 6 |
| 10s | run1 | `indeterminate` | 9673 ms | 10749 ms | false | 11 / 11 |
| 10s | run2 | `indeterminate` | 9668 ms | 10740 ms | false | 11 / 11 |
| 10s | run4 | `indeterminate` | 9653 ms | 10724 ms | false | 11 / 11 |
| 10s | run5 | `indeterminate` | 9662 ms | 10738 ms | false | 11 / 11 |
| 30s | run2 | `indeterminate` | 28983 ms | 30053 ms | false | 29 / 29 |
| 60s | run2 | `indeterminate` | 59036 ms | 60112 ms | false | 57 / 57 |

`transientMisses` was `0` for every row. `authoritativePolls` equalled `polls` for every row,
because the sentinel phase proved the cache shared, so every poll could observe the entry.

Clock-independent evidence, which does not depend on polling luck:

| requested TTL | run | max `age` Cloudflare reported | `cache-control` Cloudflare returned | readable from the writing isolate |
| --- | --- | --- | --- | --- |
| 1s | run2 | null (no read ever hit) | (none) | true |
| 1s | run3 | 0 | `max-age=14400` | true |
| 2s | run3 | 1 | `max-age=14400` | true |
| 5s | run2 | 4 | `max-age=14400` | true |
| 10s | run1 / run2 / run4 / run5 | 9 / 9 / 9 / 9 | `max-age=14400` | true |
| 30s | run2 | 28 | `max-age=14400` | true |
| 60s | run2 | 59 | `max-age=14400` | true |

**Reading these two tables together.** Every verdict is `indeterminate`, and every one of them
is the *good* kind: the poll bracket straddles the requested TTL because the entry was alive on
the poll just under it and gone on the poll just over it. At `--pollSeconds 1` the bracket is
~1 s wide, so a `honored` verdict is arithmetically unreachable — `lastHitMs` lands at
`ttl − ~330 ms` every time. The lifetime is therefore bracketed as **`[last hit, first miss]`
= [9653, 10749] ms for a requested 10 s**, and analogously at 1, 2, 5, 30 and 60 s. The
requested TTL falls inside every bracket. Per the guidance below, prefer this over the verdict
string.

The `never-cached` row at 1 s is a **polling artefact, not a TTL finding**: with
`--pollSeconds 1` the first poll fired at 1081 ms, by which time a 1 s entry had already
expired, so no poll ever observed it. `verified: true` on that same write proves the entry was
stored. Re-run at `--pollSeconds 0.25` (run3) brackets it at [977, 1300] ms.

**Two direct conclusions:**

1. **`cache.put` does not floor TTLs upward.** A 1 s entry was demonstrably gone by 1300 ms
   and a 2 s entry by 2265 ms. Cloudflare's own `age` never exceeded the requested TTL in any
   run (max `age` of 9 for a 10 s request, 59 for a 60 s request). There is no observed
   minimum above 1 s. `snapshotTtlSeconds = 10` in `workers/nextjs/src/tag-clock.ts` is
   honored as written.
2. **The returned `cache-control` header is not evidence about retention.** Every hit came
   back as `max-age=14400` regardless of whether 1, 2, 5, 10, 30 or 60 was written — including
   the positive-control read in `PUT`'s own response (`verifiedCacheControl: max-age=14400`).
   14400 s is 4 hours, Cloudflare's default zone Browser Cache TTL, so this is the **zone's
   response-header rewrite on the way out**, not what the cache stored. Retention tracked the
   requested `max-age` exactly while the header claimed 4 hours in every single case. This
   inverts the README's advice for this one field: `age` is trustworthy, the echoed
   `cache-control` is not, and anyone reusing this instrument should not read a TTL floor out
   of it. (The zone's `browser_cache_ttl` setting could not be confirmed via the API — the
   token in use lacks zone-settings read — so "14400 = default Browser Cache TTL" is the
   explanation, not a verified reading.)

### 3. Isolates per colo under load

All in colo `JNB`. Two independent counts per run: the census phase (`/identity` bursts, fresh
undici dispatcher per round) and the sentinel read phase (`/entry` bursts on the default pool).

| run | colo | census isolates | census requests | sentinel reader isolates | sentinel reads | concurrency | rounds |
| --- | --- | --- | --- | --- | --- | --- | --- |
| run1 | JNB | 4 | 256 | 28 | 512 | 64 | 4 |
| run2 | JNB | 26 | 256 | 33 | 512 | 64 | 4 |
| run3 | JNB | 19 | 128 | 28 | 256 | 64 | 2 |
| run4 | JNB | 47 | 256 | 35 | 512 | 64 | 4 |
| run5 | JNB | 65 | 1200 | 99 | 2400 | 200 | 6 |

**Highest observed lower bound: 99 distinct isolates in one colo**, from run5's sentinel phase
at `--concurrency 200`.

Treat this as a lower bound — see the limitations above; connection-to-isolate affinity in
Workers is undocumented, so the probe can only count the isolates its requests happened to
reach, never the colo's true isolate population.

Two things the spread shows that are worth carrying forward:

- **The count rises with load and with warm-up, and had not plateaued.** run1's census found
  4 isolates on the very first traffic the worker ever saw; by run4, on identical flags, the
  same census found 47. Raising concurrency 64 → 200 took it to 65 (census) / 99 (sentinel).
  Nothing here bounds the count from above.
- **The census undercounts relative to the sentinel phase in four of five runs** (4 vs 28,
  26 vs 33, 19 vs 28, 47 vs 35 — only run4 inverts). The census's fresh-dispatcher-per-round
  design, intended to *widen* sampling, sampled no wider than plain repeated bursts on a
  pooled dispatcher. Take `max(census, sentinel reader isolates)` as the run's lower bound.

## Verdict on the design

### 1. Is L1 viable as designed? **Yes.**

All five runs returned `cross-isolate-visible` with `verified: true`. A `cache.put` from one
isolate was read back by 28–99 *other* isolates in the same colo, at a hit rate of `1` in four
runs and `2386/2388` in the fifth. The Cache API is a **colo-scoped store**, not an
isolate-local one. A per-colo sentinel therefore does what `ocelhq-wvag.7` assumes: one isolate
writes it and the rest of the colo sees it.

Two qualifications that survive the result and belong in child 7:

- **The suppression window opens at ~200 ms, not at 0.** **← SUPERSEDED by `ocelhq-wvag.9`;
  it opens at 8 ms. See the follow-up section.** The earliest cross-isolate hit was
  201–251 ms after the `PUT` returned, and that number is the *runner's* round trip, so it is
  an upper bound on propagation rather than a measurement of it. Still, L1 cannot suppress a
  request that arrives before the sentinel is written and readable. For a herd, the fan-in that
  escapes L1 is whatever arrives inside that window, which is a function of request rate — it
  is **not** bounded by the miss rate in §1. This, not the 2 misses, is the real reason L2 must
  exist.
- **L1 is inert on domainless deploys.** Unchanged from the precondition section above: an
  Ocel app with no domains answers only on workers.dev, where `caches.default` is a no-op. L1
  buys such deploys exactly nothing. This is a routing property, not something this spike can
  fix.

### 2. Does `snapshotTtlSeconds = 10` hold? **Yes, exactly.**

No floor exists above 1 s. The 10 s entry was live at 9653–9673 ms and gone by 10724–10749 ms
across four runs, and Cloudflare's own `age` peaked at 9 in every one. The tag-clock's
invalidation bound ("an invalidation reaches a PoP within one TTL") is correct as written and
the constant needs no change.

One correction to make in `workers/nextjs/src/tag-clock.ts`'s vicinity if anyone reasons from
response headers there: the `cache-control` Cloudflare returns on a colo hit is
`max-age=14400` regardless of what was `put`, so it cannot be used to infer the entry's
remaining life. See §2 above.

### 3. What fan-in must L2 absorb in `ocelhq-wvag.8`?

The sizing rule the epic specifies is **§3 isolate count scaled by `1 − crossIsolateHitRate`,
not colo count**. Applying it to the measured numbers:

| input | value |
| --- | --- |
| isolate count (§3, highest observed lower bound) | 99 |
| `crossIsolateHitRate` (§1, worst of five runs) | `2386/2388` |
| `1 − crossIsolateHitRate` | `0.0008375209380234506` |
| **fan-in from L1 misses** | `99 × 0.0008375209380234506 = 0.08291457286432161` |

**Do not size L2 from that number.** It is well under one request, and in four of five runs the
rate was exactly `1`, which drives the product to `0`. What the arithmetic actually says is
that **L1 miss rate is not the term that sizes L2** — the formula degenerates when the
suppression factor is this close to total.

The term that does size L2 is the **write-visibility window** from verdict 1: the requests that
arrive in the ~200 ms before the sentinel is readable, which L1 cannot suppress at any hit rate.
This spike did not measure that, because it was not designed to — it writes once and then
reads, rather than racing N writers into a cold key. Sizing L2 needs a number this run does not
contain.

Concretely, for child 8:

- Size L2's fan-in as **`arrival rate × sentinel write-visibility latency`**, bounded above by
  the §3 isolate count (99 as a lower bound on isolates, and no isolate can contribute more
  than its own concurrent misses). Do **not** size it as one request per colo, and do not size
  it from `1 − crossIsolateHitRate`.
- The §3 count is a **lower bound and had not plateaued** — it rose from 4 to 99 as load and
  warm-up increased across five runs. Any ceiling derived from it is a floor on the real
  ceiling.
- A follow-up measurement is warranted before finalising L2's lease sizing: a cold-key burst
  that measures how many concurrent writers reach `origin()` before the first sentinel becomes
  colo-visible. That is the number child 8 is actually absorbing, and it is the one gap this
  spike leaves open. **`ocelhq-wvag.9` took it — see the follow-up section for `W`, `E(N)` and
  the sizing table child 8 should read instead of this paragraph.**

### What this contradicts in the epic

Nothing in the decision record is overturned. Decision 12's premise held: the colo cache is
shared, so L1 is real. The one framing correction is the sizing formula above — the epic
anticipated `partially-visible` and a meaningful suppression factor to divide by, and the
measurement came back at effectively full sharing, which makes that factor the wrong lever.

## Follow-up: the L1 write-visibility window (`ocelhq-wvag.9`)

Status: **MEASURED** on 2026-08-04 against two zone-routed deploys of the same package, at
`probe.ocel.site`, all runs reaching colo `JNB`. Headline: **`W = 8 ms`**, and the first thing
measured was not `W` at all.

The spike above ends with "a follow-up measurement is warranted … a cold-key burst that
measures how many concurrent writers reach `origin()` before the first sentinel becomes
colo-visible." `scripts/race.ts` is that measurement. It races claimers into a key nobody has
written, each running the exact `match`-then-`put` that `admitRefresh` runs, which the
write-then-read runner above structurally cannot do.

**This section was rewritten after a review round and a second deploy.** The first pass
reported `W = 10 ms` off a sweep whose steps were 0, 5, 10 — a 5 ms bin — and reported an
`E(N)` whose burst phase had two defects that a re-measurement exposed. What the first pass got
right survived unchanged: the key-scope control, the gap sweep's design, and the conclusion
that L1 leaks a synchronized herd at roughly the isolate count. What it got wrong is recorded
in §8 rather than quietly overwritten.

### Run metadata

| field | value |
| --- | --- |
| date | 2026-08-04, ~19:2x–20:0x UTC (first pass) and ~23:2x–23:5x UTC (re-measurement) |
| probe host | `probe.ocel.site` (zone route `probe.ocel.site/*`), plus one control run on `probe.ocel.dev`. `ocel.site` was chosen because `ocel.dev` carries the `*.ocel.dev/*` wildcard worker route |
| deploy | the same `ocel-cache-probe` script and the same two committed zone routes as the runs above; no DNS record was created — both zones already carry a proxied `*` AAAA record. Torn down after each pass, verified by listing the account's scripts and both zones' routes |
| runner location | one host outside Cloudflare; every request in every run landed in colo `JNB` |
| raw run files | `workers/cache-probe/runs/race-*.json` (first pass) and `race2-*.json` (re-measurement), gitignored, on the machine that ran them |

The re-measurement's runs, all `node scripts/race.ts --base https://probe.ocel.site`:

| run | invocation |
| --- | --- |
| control | `--phase control --sockets 48` |
| gap-fine-1, gap-fine-2 | `--phase gap --trials 250 --deltas 0,5,6,7,8,9,10,12,15 --sockets 16` |
| burst-3 … burst-6 | `--phase burst --trials 100 --sizes 2,8,32,128 --window 10 --isolates 99` (`--window 8` on burst-6) |
| diagnostics | three ad-hoc runs isolating the fixed-pool defect; see §8 |

A deployment note, matching the one recorded above for `ocel.dev`: for the first ~3 minutes
after `wrangler deploy`, a fraction of requests to `probe.ocel.site` returned `522` — the zone
route had not reached every edge machine and the request fell through to the AAAA record's
dummy origin. The re-measurement saw 29/60, then 11/60, then clean. Every run below was taken
after three consecutive clean bursts of 60. The runner's own preflight repeats that check with
200 requests and aborts on any non-200.

### 0. The key-scope control, which gates everything else — **P0 held**

Every colo-cache key in `workers/nextjs` is on a **synthetic hostname that belongs to no
zone**: `https://cache.ocel/…` (entries), `https://refresh.ocel/…` (the L1 sentinel),
`https://isr.ocel/…` (the tag-clock front), `https://image.ocel/…` (the optimized-image tier).
The spike above only ever proved an **on-zone** key (`/__cache-probe/<run>` on the serving
origin). Cloudflare documents no zone-matching rule for `caches.default`, but absence of a
documented rule is not evidence of absence of the behaviour, and every local test runs against
Miniflare, which stores any hostname happily. If off-zone keys were silently discarded, all
four tiers would be no-ops in production — silently, failing open.

`GET /control` writes and reads back the same record under both shapes, then fans out
foreign-isolate reads at each.

| run | host | on-zone cross-isolate hits | off-zone cross-isolate hits | reader isolates (on/off) |
| --- | --- | --- | --- | --- |
| control-1 | `probe.ocel.site` | 19/19 | 21/21 | 9 / 5 |
| control-2 | `probe.ocel.site` | 35/35 | 33/33 | 13 / 14 |
| control-3 | `probe.ocel.site` | 39/39 | 34/34 | 15 / 17 |
| control-dev | `probe.ocel.dev` | 37/37 | 36/36 | 16 / 16 |
| control (re-measurement) | `probe.ocel.site` | 38/38 | 48/48 | 19 / 29 |

**Verdict `both-scopes-visible` in all five runs, 340 of 340 cross-isolate reads hit, and
`verified: true` on every write.** An off-zone synthetic key stores and is colo-visible exactly
as an on-zone key is. **The four cache tiers are live in production.** This is a negative
result and it was the one worth being most afraid of; it is recorded first because an
`offzone-inert` verdict here would have superseded this whole issue with a P1 defect.

### 1. `W`, the write-visibility window — 8 ms

Two racers per trial on a **fresh UUID key**, the second fired a driver-imposed Δ after the
first was *sent*. Δ is imposed on the driver's single clock and both racers pay the same round
trip, **so the driver's RTT cancels out of Δ** rather than having to be subtracted from it.
No two Worker clocks are differenced anywhere, preserving the rule the runner above sets.

The statistic is `P(second claimed | second on a different isolate, same colo, first claimed)`.
The first pass swept 0, 5, 10, 15, 20, 25, 30, 40, 50, 75, 100, 150, 200, 300, 500, 1000 (200
trials each) and again at 0, 5, 10, 15, 20, 25, 30, 50 — which put the drop inside the 5–10 ms
bin and reported its upper edge. The re-measurement swept that bin at 1 ms resolution, 250
trials per Δ.

| Δ (ms) | run 1 decidable / claims / rate | run 2 decidable / claims / rate |
| --- | --- | --- |
| 0 | 241 / 200 / **0.8299** | 231 / 174 / **0.7532** |
| 5 | 247 / 87 / **0.3522** | 250 / 68 / **0.2720** |
| 6 | 248 / 53 / **0.2137** | 242 / 33 / **0.1364** |
| 7 | 248 / 41 / **0.1653** | 241 / 35 / **0.1452** |
| 8 | 250 / 10 / **0.0400** | 248 / 6 / **0.0242** |
| 9 | 244 / 6 / 0.0246 | 249 / 4 / 0.0161 |
| 10 | 239 / 1 / 0.0042 | 228 / 13 / 0.0570 |
| 12 | 235 / 2 / 0.0085 | 248 / 0 / 0.0000 |
| 15 | 222 / 0 / 0.0000 | 242 / 3 / 0.0124 |

**`W = 8 ms`, verdict `measured`, in both runs independently**, taken as the smallest Δ whose
rate falls below 0.05 *and stays below it at the next Δ*. Defined more fully as: the elapsed
time after a claimer *begins its `match`* beyond which a second isolate's `match` hits with
probability ≥ 0.95. Deliberately end-to-end over the claim path, so it includes the leader's
`put` duration as well as propagation — a racer arriving during the leader's `put` escapes too,
and any definition anchored on "after `cache.put` returns" would undercount.

Two caveats that belong with a 1 ms figure and did not matter at 5 ms resolution:

- **The achieved Δ is not the nominal Δ.** The driver imposes the gap with `setTimeout`, whose
  floor is about a millisecond: at nominal Δ=0 the achieved median is **1.2 ms**, and at
  nominal 5…15 the achieved median runs **0.7–0.8 ms below** nominal. So the Δ=0 row is really
  "≈1.2 ms apart" and `W` against achieved Δ is **8.2 ms**. The nominal figure is the
  conservative one and is what the sizing below uses.
- **Run 2's Δ=10 row is 0.0570, above the 0.05 threshold**, while its Δ=9 and Δ=12 rows are
  0.0161 and 0.0000. That is sampling noise at 13 claims in 228 trials, not a second window; it
  did not move the verdict, because 8 and 9 both suppress. Recorded rather than smoothed.

Zero trials discarded in either run, out of 2 250 attempted apiece. `mixed-colo` was zero
(`foreignColoObservations: 0` in the spike above was not a fluke of that instrument), and so
was `zero-claims` — but see §7: neither of those zeros is evidence of anything. Trials excluded
as `same-isolate` totalled 68 and 57 (production collapses those in L0 before the sentinel is
consulted, so they are not L1's to measure); `leader-did-not-claim` totalled 8 and 14, nearly
all at Δ=0 where "leader" is arbitrary. **Those last two buckets are the live ones**, and their
non-zero counts are what demonstrates the classifier is not inert.

### 2. The driver's RTT, and re-reading the spike's 201–251 ms

The spike above reports "first cross-isolate hit 201–251 ms after the write" and correctly
labels it the *runner's* round trip rather than a propagation measurement — but it never
recorded what that round trip was, so the number could not be decomposed. It is now recorded.

| run | median RTT to `/identity` | p10 |
| --- | --- | --- |
| gap-1 | 68.9 ms | 66.1 ms |
| gap-2 | 68.4 ms | 67.5 ms |
| burst-1 | 67.3 ms | 66.3 ms |
| burst-2 | 62.8 ms | 62.3 ms |
| re-measurement (6 runs) | 62.6–68.1 ms | 61.4–67.8 ms |

Sequential, on one already-warm socket, so it is a latency and not a queueing delay.

**The honest reading is that `W` is not visible in the 201 ms at all.** The spike's sentinel
phase started its clock when the `PUT` **returned to the driver** — one full round trip after
the write executed at the edge — so an 8 ms edge-side window had already elapsed, twice over,
before that clock started. Adding it back as a term double-counts it. What the 201 ms contains
is one read round trip (~65 ms) plus ~135 ms of the instrument: the first read burst was 64
concurrent requests on the default undici pool's **cold** sockets, so 64 TLS handshakes and
64-way queueing are inside the number. The residual is a subtraction, not a measurement.

So the sentence in verdict 1 above — "the suppression window opens at ~200 ms, not at 0" —
**overstates the window by a factor of about twenty-five.** The window opens at 8 ms.

This falsifies prediction P5 (that `W` would come out near `201 ms − RTT`). The two methods
disagree by ~135 ms. Cold sockets and burst queueing are **the most likely explanation, and it
is untested**: nobody re-ran the old instrument with pre-warmed sockets to watch the 201 ms
fall to ~70 ms. Per this stack's own rule — an undecidable result reports `inconclusive` rather
than guessing — that is where it stays. The gap sweep is the primary instrument here because it
structurally cannot contain either effect, not because its answer is the convenient one.

### 3. `E(N)`, escapes per colo per stale event

`N` racers on **pre-warmed** sockets at one cold key, 100 trials per `N`, each trial drawn as a
**rotating window over a pool of `N + 16` sockets**. Escapes are collapsed by isolate, because
production runs `refreshOnce` (L0) inside `admitRefresh`, so two concurrent requests on one
isolate never both reach the sentinel; `rawClaims` is shown only to demonstrate the collapse
happened. **Send dispersion is measured at the socket write** (undici's
`undici:client:sendHeaders`), not in the driver's dispatch loop — see §8 for why that
distinction voided the first pass's numbers.

| N | run | counted | single-isolate | escapes min / p10 / median / p90 / max | mean | raw claims median | distinct isolates | dispersion median / max | E/I |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 2 | 3 | 100 | 0 | 1 / 1 / **2** / 2 / 2 | 1.60 | 2 | 2–2 | 0.06 / 1.26 ms | 0.80 |
| 2 | 4 | 89 | 11 | 1 / 1 / **2** / 2 / 2 | 1.82 | 2 | 2–2 | 0.06 / 1.19 ms | 0.91 |
| 2 | 5 | 92 | 8 | 1 / 1 / **2** / 2 / 2 | 1.83 | 2 | 2–2 | 0.06 / 1.15 ms | 0.91 |
| 2 | 6 | 100 | 0 | 1 / 1 / **2** / 2 / 2 | 1.73 | 2 | 2–2 | 0.06 / 0.16 ms | 0.86 |
| 8 | 3 | 100 | 0 | 2 / 3 / **7** / 8 / 8 | 5.95 | 7 | 7–8 | 0.14 / 5.05 ms | 0.74 |
| 8 | 4 | 100 | 0 | 3 / 4 / **6** / 7 / 7 | 5.69 | 7 | 6–7 | 0.15 / 1.50 ms | 0.81 |
| 8 | 5 | 100 | 0 | 4 / 5 / **6** / 7 / 8 | 5.96 | 6 | 6–8 | 0.14 / 1.27 ms | 0.74 |
| 8 | 6 | 100 | 0 | 2 / 4 / **5** / 6 / 7 | 4.85 | 5 | 7–8 | 0.14 / 1.64 ms | 0.61 |
| 32 | 3 | 100 | 0 | 12 / 17 / **19** / 20 / 22 | 18.45 | 22 | 21–26 | 0.38 / 1.86 ms | 0.71 |
| 32 | 4 | 100 | 0 | 15 / 18 / **19** / 21 / 24 | 19.40 | 24 | 20–26 | 0.35 / 1.77 ms | 0.75 |
| 32 | 5 | 100 | 0 | 10 / 19 / **23** / 26 / 27 | 22.55 | 26 | 25–28 | 0.38 / 2.08 ms | 0.81 |
| 32 | 6 | 100 | 0 | 13 / 15 / **18** / 22 / 24 | 18.07 | 22 | 22–25 | 0.39 / 2.21 ms | 0.72 |
| 128 | 3 | 100 | 0 | 24 / 47 / **57** / 68 / 72 | 56.23 | 86 | 63–77 | 1.20 / 4.13 ms | 0.73 |
| 128 | 4 | 100 | 0 | 35 / 47 / **56** / 64 / 70 | 55.63 | 88 | 69–81 | 1.15 / 3.34 ms | 0.69 |
| 128 | 5 | 100 | 0 | 44 / 55 / **63** / 73 / 81 | 63.17 | 98 | 76–81 | 1.22 / 3.70 ms | 0.78 |
| 128 | 6 | 100 | 0 | 29 / 42 / **51** / 58 / 63 | 50.28 | 78 | 66–73 | 1.14 / 4.10 ms | 0.69 |

Zero trials discarded, out of 400 attempted per run. `E/I` is the mean escape count over the
run's largest `distinctIsolates`.

**Escapes are not printed as a lower bound**, and unlike the first pass that verdict now rests
on a dispersion that can actually exceed the window: median send spread is 0.06 ms at `N=2` and
1.14–1.22 ms at `N=128`, with a worst single trial of 5.05 ms — the same order as `W = 8 ms`,
not two orders below it. At `N = 128` the guard is within a factor of seven of firing, so
`E(128)` should be read as a slight under-count rather than as an exact figure.

**The two phases now cross-validate, which they did not before.** The gap sweep measures
`P(second claims | Δ≈0) = 0.75–0.83` independently of the burst; that predicts a mean of
`1 + 0.75…0.83 = 1.75–1.83` escapes at `N = 2`, and the burst measures **1.60, 1.73, 1.82,
1.83**. Two instruments with different failure modes agreeing on a number neither was tuned to
produce is the strongest evidence in this document.

The shape that matters: at `N = 128` arriving effectively simultaneously, **about three
quarters of the isolates the burst touched each escaped** — `E/I` ran 0.69–0.78 across four
runs, never 1.00. L1 is not suppressing a synchronized herd to one; it suppresses it to roughly
three quarters of the isolate count.

`I_colo`: this re-measurement's highest `distinctIsolates` was **81** at `N = 128`, below the
spike's 99, so the lower bound stays at **`I_colo ≥ 99`**. It is still a lower bound and still
had not plateaued — 128 sockets reached at most 81 isolates, so the pool, not the colo, was the
limit.

### 4. Sizing L2 — what `ocelhq-wvag.8` should read

```
E = min(1 + λ_colo · W, I_colo)          escapes per colo per stale event
F = C · E                                 L2 fan-in per stale event
R = C · E / refreshSentinelTtlSeconds     sustained L2 request rate
```

`λ_colo`, the arrival rate on one hot route in one colo, is **not measured here and is not
measurable here**: it is a property of the operator's traffic, not of Cloudflare. It is a
**parameter**. Saying so is part of the deliverable — otherwise PR 8 hard-codes a guess.

At `W = 0.008 s`, `I_colo ≥ 99`, `C ≈ 300` colos carrying traffic, `refreshSentinelTtlSeconds = 5`:

| `λ_colo` (rps) | `E` | `F` | `R` (rps) | vs 500 rps/DO | vs 1000 rps/DO |
| --- | --- | --- | --- | --- | --- |
| 1 | 1.01 | 302 | **60** | under | under |
| 10 | 1.08 | 324 | **65** | under | under |
| 100 | 1.80 | 540 | **108** | under | under |
| 1 000 | 9.00 | 2 700 | **540** | **over** | under |

`R = 60 + 0.48 · λ_colo`, so it crosses 500 rps at `λ_colo ≈ 917` and 1 000 rps at
`λ_colo ≈ 1 958` — a route taking ~275 000 rps globally before the first ceiling. **For smooth
traffic, one DO per route survives.**

Two corrections to `ocelhq-wvag.8`'s own arithmetic fall out of this:

- **The baseline is 60 rps, not 30.** `.8` computes "300 colos / sentinel TTL ≈ 30 rps",
  which implies a TTL of 10. PR 7 shipped `refreshSentinelTtlSeconds = 5`
  (`workers/nextjs/src/cache.ts`), so one escape per colo is `300/5 = 60 rps`.
- **`E` is not 1.** It is `1 + λ_colo · W`, and under a *synchronized* herd it is not that
  either.

**The synchronized-herd case is where `.8`'s rejection of sharding needs a second look.** The
linear term assumes arrivals spread over `W`. §3 measured what happens when they are not.
Synchronized arrival is not hypothetical for this system — every colo's sentinel expires one
TTL after it was taken, so a route hot everywhere re-admits on a shared schedule.

Two numbers bound that regime, and **both are extrapolations from a measured ratio onto an
inherited isolate count, not measurements.** Neither was observed directly, because no run here
reached 99 isolates:

| basis | `E` | `R = 300 · E / 5` |
| --- | --- | --- |
| the **measured** `E/I` ratio (0.69–0.78) applied to `I_colo ≥ 99` | 68–77 | **~4 100–4 640 rps** |
| the **worst-case bound** `E = I_colo`, which no run reproduced | 99 | **5 940 rps** |

Even the lower row is **8× the 1 000 rps per-DO ceiling and 16× the conservative 500**, so the
conclusion does not depend on which row is taken: **a synchronized herd exceeds one DO per
route by an order of magnitude.** But 5 940 is a bound, not a measurement, and PR 8 should
design against the range and label the row it uses. `I_colo = 99` is itself a lower bound, so
both rows are floors.

### 5. Predictions — which held, which did not

| # | prediction | outcome |
| --- | --- | --- |
| P0 | an off-zone synthetic key stores and is colo-visible identically to an on-zone key | **held** — 340/340, five runs, two zones |
| P1 | second-racer claim probability decreases monotonically with Δ and reaches <0.05 by Δ≈250 ms | **held, and by 30×** — monotone through the 1 ms sweep, and <0.05 by Δ=8 ms |
| P2 | at Δ=0 with ≥2 distinct isolates, both racers claim | **held** — 0.75 and 0.83 at Δ=0; and at N=128, ~0.73 of the isolates touched |
| P3 | escapes ≤ distinct isolates in every trial after collapse | **not a finding — guaranteed by construction.** Escapes is the isolate-set size over a subset of the outcomes, so the inequality is set theory and the detector that "reported" it could not fail. It has been deleted. The live check is `outcomeOf`'s comparison of the worker-echoed key and seq against what was sent, and **it never fired** in any run |
| P4 | escapes ≥ 1 in every trial | **not a finding — guaranteed by construction.** The globally first `match` on a fresh UUID key must miss and therefore claim. The assertion is kept, but a zero here is a theorem; see §7 |
| P5 | `W ≈ 201 ms − median RTT`, within the RTT's own spread | **FALSIFIED** — `W` is 8 ms, not ~135 ms. See §2; the residual is most likely the older instrument's cold sockets and 64-way burst queueing, but that explanation was never tested, so the disagreement is reported rather than resolved |

### 6. Staleness clause

**`W` was measured against a `match`-then-`put` claim with no compare-and-set
(`workers/nextjs/src/cache.ts`, `claimSentinel`) and against `refreshSentinelTtlSeconds = 5`
(same file). If either changes, this number is void.** `workers/cache-probe/src/race.ts`
carries the same warning at the mirror itself.

One comment in `workers/nextjs/src/cache.ts` — the one justifying
`refreshSentinelTtlSeconds = 5` by citing "a sentinel becoming readable from sibling isolates
at ~200 ms" — is now **wrong by a factor of twenty-five**. The constant it justifies is
unaffected (5 s is still far above 8 ms), only the reasoning is. That edit was made under
`ocelhq-wvag.16` rather than `.8`, since `.16` is where the same file gained
`admissionJitterMs`, which is derived from this number and carries the same clause.

### 7. What these numbers do not cover

- **One colo, one runner.** `W` and `E` are JNB's. `C ≈ 300` is an assumption about
  Cloudflare's fleet, not a measurement taken here.
- **Three of the burst's four buckets are structurally unreachable, and their zeros are not
  evidence.** `zero-claims` cannot fire because the globally first `match` on a fresh key must
  miss; `mixed-colo` cannot fire from a single driver host, which reaches one colo;
  `single-isolate` is live but rare. The first pass cited all three zeros as validation. The
  buckets that demonstrably fire are the gap sweep's `leader-did-not-claim` and `same-isolate`,
  and now the burst's `single-isolate` at `N=2` (8–11 per 100 trials, which a fixed pool never
  produced) — those are the liveness evidence.
- **Send-side dispersion only, and it is no longer negligible.** The 0.06–1.22 ms median
  dispersion is the spread of the driver's *socket writes*. Server-side arrival spread is still
  not measured, and it remains a plausible contributor to `E/I ≈ 0.73` rather than 1.0 at
  `N = 128`.
- **Cross-isolate visibility is NOT uniform within a colo.** Measured directly while diagnosing
  §8: on one pair of sockets both racers claimed 31 of 40 times at zero separation, while on
  another pair the same socket won 79 of 80 regardless of dispatch order. `W = 8 ms` is a
  population statistic over pairs, not a property every pair has. Anything reasoning about a
  *specific* pair of isolates is outside what this measures.
- **`I_colo` is a lower bound and has never plateaued.** 128 sockets reached at most 81
  isolates here; the spike above reached 99 with 200-way concurrency.
- **The Δ sweep's resolution is 1 ms and its floor is `setTimeout`'s.** Achieved Δ tracks
  nominal Δ to within ~1 ms, so `W` is 8 ms nominal and 8.2 ms achieved. Anything finer needs a
  different pacing mechanism.
- **`λ_colo` is an operator parameter**, restated here because every conclusion in §4 is
  conditional on it.

### 8. What the first pass got wrong, and how it was found

Recorded rather than overwritten, because both defects are the class this stack keeps
producing: a green signal from a detector that could not fire.

- **`dispersionMs` measured a JS loop, so the burst's only concurrency guard was dead.** It was
  `max − min` over timestamps taken in the driver's own `racers.map(...)` callback — before
  undici had written a byte, since `request()` queues the write and resumes it off the loop. It
  read 0.03–0.26 ms at `N = 2…128`, so `dispersion.median > windowMs` was false in every real
  run and the phase printed "not a lower bound" on eight rows, on evidence about a for-loop.
  Now taken from `undici:client:sendHeaders`, which publishes immediately before the request
  head goes to the socket. The same bursts then measure 0.06–1.22 ms, and 5.05 ms in the worst
  single trial — four to twenty times larger, and within reach of `W`.
- **A fixed socket pool made 100 trials into one draw repeated 100 times.** A socket pins to an
  isolate for the pool's life, so `N` sockets meant `N` fixed isolates. The tell was in the
  first pass's own numbers, and it was reported as two "independent reproductions": at `N = 2`,
  run 1 gave escapes min/p10/median/p90/max **all = 1** over 100 trials while run 2 gave a
  median of 2 — mutually incompatible, and run 1 incompatible with the gap sweep's 0.80 at
  vanishing probability.

  It was diagnosed rather than assumed. The re-measurement reproduced the `1` twice more, and
  then the raw trials showed **seq 1 claiming in 100 of 100 trials and seq 0 in 1** — the racer
  sent *first* was the one suppressed, which no propagation model explains. A direct experiment
  then showed the sole claimer was the same socket in 79 of 80 trials **whichever of the two
  was dispatched first**, so the winner was a property of the connection and not of the race;
  and a different pool of two had both racers claiming 31 of 40 times at the same separation.
  The pool is now `N + 16` wide with a rotating window, four runs agree, and `E(2)` lands on
  the gap sweep's independently measured value.

  The finding underneath the defect is the §7 bullet: **visibility latency varies by isolate
  pair**, and a fixed pool samples one pair and prints it without a spread.

Three smaller corrections to the first pass's arithmetic and framing: the gap sweep was
**4 800** trials (16 Δs + 8 Δs, at 200 each), not the 6 400 stated twice; P4's total was
**5 600** (4 800 + 800), not 7 200; and `leader-did-not-claim` totalled 16, not 15. The
re-measurement adds 4 500 gap trials and 1 600 burst trials on top.

## Follow-up: the admission-jitter sweep (`ocelhq-wvag.16`)

Status: **MEASURED** on 2026-08-05 against a third zone-routed deploy of the same package, at
`probe.ocel.site`, all runs reaching colo `JNB`. Headline: **a jittered admission delay takes
`E(128)` from 54.8–62.0 to 1.41–1.46, and it does not plateau above 1.**

The section above sizes L2 against a *sustained* rate and finds one Durable Object per route
survives smooth traffic. That was the wrong constraint. A route goes stale at one wall-clock
instant and every colo sees it simultaneously, so the fan-in per stale event is
`F = C · E ≈ 300 × 55…62 ≈ 17 000–19 000` requests arriving within a few hundred milliseconds —
against an object Cloudflare rates at "approximately 500-1,000 requests per second for simple
operations". That is ~20 s of queueing: overload, not slow success.

**And the synchronization is self-inflicted.** `claimSentinel` fires the instant a request
observes staleness and `settleSentinel` re-arms exactly one `refreshSentinelTtlSeconds` later,
colo-wide. Nothing in the design requires the admission attempts to be simultaneous. So the
proposal `ocelhq-wvag.16` puts to this measurement is not to size for the burst but to stop
producing it: wait a uniform draw from `[0, J)` before claiming.

The derivation is deliberately **λ-free**, because §4 established `λ_colo` is an operator
parameter and a constant derived from a guess at it would be a guess wearing a measurement's
clothes. L0 (`refreshOnce`) already collapses each isolate to one in-flight admission and holds
that entry across the wait, so the claimant pool inside one jitter window cannot exceed
`I_colo`:

```
claims_per_colo ≈ 1 + I_colo · W / J        J ≥ I_colo · W
```

At `I_colo ≥ 99` and `W = 8 ms` that is `J ≥ 0.79 s`, rounded to **`J = 1000 ms`**, predicting
1.79 claims per colo whatever λ is.

### 9. Run metadata

| | |
| --- | --- |
| date | 2026-08-05, ~11:0x–11:3x UTC+3 |
| probe host | `probe.ocel.site` (zone route `probe.ocel.site/*`); no DNS record was created — the zone already carries a proxied `*` AAAA record |
| deploy | the same `ocel-cache-probe` script and the same two committed zone routes as the passes above. Torn down afterwards, verified by listing the account's scripts and both zones' routes |
| runner location | one host outside Cloudflare; every request in every run landed in colo `JNB` |
| raw run files | `workers/cache-probe/runs/race3-*.json`, gitignored, on the machine that ran them |

Route propagation behaved as before: 34/60, 15/60, 2/60, 1/60 non-200 in successive preflight
bursts of 60, then four consecutive clean bursts before any run was taken. The runner's own
preflight repeats that check with 200 requests and aborts on any non-200; both runs passed it.

The key-scope control was re-taken on this deploy first: **`both-scopes-visible`**, on-zone
40/40 and off-zone 34/34 cross-isolate hits over 16 and 13 reader isolates, `verified: true` on
both writes. Everything below is conditional on that, as §0 is.

Both runs are `node scripts/race.ts --base https://probe.ocel.site --phase burst --trials 100
--sizes 128 --window 8 --isolates 99 --jitters 0,100,250,500,1000,2000`. Each racer's **worker**
draws `U[0, J)` and sleeps it before claiming — not the driver, because a driver-imposed delay
would also spread the *arrivals*, and arrival spread suppresses claims by itself, which is the
term this instrument has to hold constant while the claim times move.

### 10. `E(128)` against `J` — the sweep

100 trials per cell, `N = 128` racers drawn as a rotating window over a pool of 144 sockets,
escapes collapsed by isolate as in §3.

| `J` (ms) | run | counted | escapes min / p10 / median / p90 / max | **mean** | late escapes mean / max | raw claims median | isolates median / max | dispersion median / p90 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 0 | 1 | 100 | 17 / 47 / **54** / 63 / 72 | **54.79** | 0 / 0 † | 81 | 77 / 84 | 1.21 / 2.50 ms |
| 0 | 2 | 100 | 26 / 53 / **63** / 74 / 79 | **61.98** | 0 / 0 † | 83 | 86 / 89 | 1.39 / 2.39 ms |
| 100 | 1 | 100 | 1 / 2 / **4** / 6 / 16 | **3.81** | 0.14 / 6 | 4 | 72 / 78 | 1.25 / 2.23 ms |
| 100 | 2 | 100 | 1 / 2 / **3** / 6 / 12 | **3.79** | 0.05 / 1 | 3 | 78 / 81 | 1.22 / 2.34 ms |
| 250 | 1 | 100 | 1 / 1 / **2** / 4 / 5 | **2.52** | 0.11 / 1 | 2 | 76 / 80 | 1.25 / 2.31 ms |
| 250 | 2 | 100 | 1 / 1 / **2** / 4 / 7 | **2.31** | 0.05 / 1 | 2 | 83 / 90 | 1.22 / 2.25 ms |
| 500 | 1 | 100 | 1 / 1 / **1** / 3 / 4 | **1.69** | 0.06 / 2 | 1 | 79 / 81 | 1.22 / 2.19 ms |
| 500 | 2 | 100 | 1 / 1 / **2** / 4 / 7 | **2.05** | 0.12 / 4 | 2 | 88 / 90 | 1.24 / 2.16 ms |
| **1000** | 1 | 100 | 1 / 1 / **1** / 2 / 4 | **1.41** | 0.07 / 1 | 1 | 75 / 80 | 1.33 / 2.13 ms |
| **1000** | 2 | 100 | 1 / 1 / **1** / 2 / 3 | **1.46** | 0.05 / 1 | 1 | 82 / 84 | 1.24 / 2.27 ms |
| 2000 | 1 | 100 | 1 / 1 / **1** / 2 / 3 | **1.21** | 0.03 / 1 | 1 | 73 / 79 | 1.52 / 116.6 ms ‡ |
| 2000 | 2 | 100 | 1 / 1 / **1** / 2 / 3 | **1.26** | 0.07 / 1 | 1 | 81 / 86 | 1.51 / 114.5 ms ‡ |

**Zero trials discarded, 600 attempted per run, 1 200 in total.** `single-isolate` was 0 and
`mixed-colo` was 0 in every cell — see §12, neither is evidence. `zero-claims` was 0, which is a
theorem, not a result.

† **The `late escapes` zero at `J = 0` is structurally unreachable and is not a finding.** Every
racer draws exactly 0, so no draw can exceed the first claim's draw by more than `W`; the
detector cannot fire in that row. It is live in every `J > 0` row and does fire there, at means
of 0.03–0.14 and maxima of 1–6. That non-zero is what demonstrates it is not inert.

‡ At `J = 2000` the driver's own send dispersion breaks down: 144 sockets each held open for up
to two seconds, and the p90 spread jumps from ~2.3 ms to ~115 ms. That is the instrument, not
the cache, and it exceeds `W` in a minority of trials — so the `J = 2000` escape counts are a
slight **under**-count. It does not touch the `J = 1000` row, whose dispersion max is 6.1 ms.

### 11. What the sweep says

**P1 held, and by a wide margin.** The gate was `E ≤ 3` at `N = 128` with `J = 1000 ms`, against
the 50–63 measured without jitter. Measured: **mean 1.41 and 1.46, median 1, p90 2, max 3–4 over
200 trials**, on two independent runs. `E` fell by a factor of 39–42.

**No plateau above 1, and no blind-pair floor is visible.** `E` is still falling at `J = 2000`
(1.21, 1.26), and the mean is approaching 1 from above rather than stalling. §7 recorded that
cross-isolate visibility is *not* uniform inside a colo — one socket pair had both racers
claiming 31 of 40 times at zero separation while another had one socket winning 79 of 80
regardless of dispatch order — and if a persistent mutually-blind population existed at rate
`p`, `E` would floor at `p · I_colo` however wide `J` grew. `lateEscapes` is what tests for it:
escapes whose draw put them more than `W` after the trial's **first** claim, which is the claim
that has had the longest to propagate. At `J = 2000` it means 0.03 and 0.07, so any such floor
is **below 0.07 escapes per colo per stale event**, i.e. `p ≲ 0.001` against `I ≈ 81`. Whatever
the pairwise non-uniformity is, it does not survive being averaged over a rotating pool.

**The λ-free bound is conservative at every `J`, and converges.** Taking `I` as each cell's
median distinct isolates:

| `J` (ms) | `1 + I·W/J` predicted (run 1 / run 2) | measured mean (run 1 / run 2) |
| --- | --- | --- |
| 100 | 6.76 / 7.24 | 3.81 / 3.79 |
| 250 | 3.43 / 3.66 | 2.52 / 2.31 |
| 500 | 2.26 / 2.41 | 1.69 / 2.05 |
| 1000 | 1.60 / 1.66 | 1.41 / 1.46 |
| 2000 | 1.29 / 1.32 | 1.21 / 1.26 |

It over-predicts everywhere, by 1.8× at `J = 100` narrowing to 1.05× at `J = 2000` — as it
should, since it assumes every isolate whose draw lands inside the leader's window escapes,
while in practice some of them are suppressed by a *different* isolate's earlier claim. **A
constant chosen from this bound is chosen conservatively.**

**What that does to `ocelhq-wvag.8`.** At `E = 1.41–1.46` and `C ≈ 300`:

```
F = C · E ≈ 425–440 requests per stale event, spread over J = 1 s
R = C · E / refreshSentinelTtlSeconds ≈ 85–88 rps sustained
```

against 17 000–19 000 in a few hundred milliseconds before. Both are under the conservative
500 rps figure, so **one Durable Object per route holds — conditional on the jitter.** The
sizing table of §4 is not printed on a jittered sweep and the runner refuses to: `E = 1 + λ·W`
models the un-jittered path, and attaching it to jittered rows would be exactly the kind of
green number this document keeps having to retract.

### 12. What these numbers do not cover

- **One colo, one runner, `N = 128`.** As §7. `J` was swept; `N` was not.
- **The probe has no L0, and that biases `E` upward — conservatively.** 128 racers reached 72–90
  isolates, so ~1.5 requests per isolate, each drawing its **own** delay; production's
  `refreshOnce` collapses an isolate to one draw and holds it across the wait. The minimum of
  several draws is stochastically earlier than a single draw, so more of the probe's isolates
  land near the front of the window than production's would. `E_probe ≥ E_production`. (Raw
  claims track escapes almost exactly at every `J > 0` — 3.85 vs 3.79, 1.47 vs 1.46 — so an
  isolate that claimed twice was rare: the second request usually found its own isolate's claim.)
- **`single-isolate` and `mixed-colo` are structurally unreachable here and their zeros are not
  evidence**, exactly as in §7. `zero-claims` likewise: the globally first `match` on a fresh key
  must miss. The detectors that demonstrably fire in this pass are `lateEscapes` at `J > 0` and
  the two new jitter guards below.
- **The jitter guards are live and were mutation-checked, not assumed.** Each racer echoes the
  delay it drew; the run aborts if the draws do not straddle `J/2` (a worker that ignored the
  parameter) or if a reported delay exceeds the request the driver timed around it (a delay
  reported but not slept). Both were confirmed to fail against a deliberately broken worker
  before the runs were taken. Neither fired in either run: draws were uniform over `[0, J)` to
  three digits (`J = 1000`: min 0.12, p10 98–102, median 499–506, p90 899–901, max 999.9 ms).
- **Send-side dispersion only, and at `J = 2000` it is no longer small.** See ‡ above.
- **`I_colo` is still a lower bound.** This pass's highest was **90**, above `.9`'s 81 and still
  below the spike's 99, and still not plateaued.
- **The delay is measured on the claim path, not on the serving path.** That the wait costs no
  user-visible latency is a property of *where* production puts it — inside `waitUntil`, behind
  an already-served stale response — and is asserted by `workers/nextjs`' tests, not by this.

### 13. Staleness clause

`E(J)` was measured against **the same claim primitive `W` was**: `match` then `put` on a miss,
no compare-and-set, `refreshSentinelTtlSeconds = 5`
(`workers/nextjs/src/cache.ts`, `claimSentinel`) — and against a delay drawn uniformly in
`[0, J)` immediately before that `match`. **Changing the claim's shape, the sentinel TTL, or the
place the delay is taken voids these numbers, and voids `W` with them.** The dependency runs
both ways and `workers/nextjs/src/cache.ts` now says so at both constants.

### 14. Prediction

| # | prediction | outcome |
| --- | --- | --- |
| P1 (jitter) | with `J = 1000 ms`, `E` at `N = 128` simultaneous racers in one colo is ≤ 3, against the 50–63 measured without jitter | **held** — mean 1.41 and 1.46, median 1, p90 2, max 4, on two independent runs of 100 trials. `E` approaches 1 rather than plateauing, and no mutually-blind floor is detectable above 0.07 escapes per stale event |

## Cleanup

This package is a spike. Once the findings above are recorded and children 8 and 16 have
landed against them, `workers/cache-probe` should be deleted; this document is the artefact
that survives. It was `7 and 8` until `ocelhq-wvag.9` re-used the package for the
write-visibility measurement above, which the write-then-read runner could not take, and
`ocelhq-wvag.16` re-used it again for the admission-jitter sweep.
