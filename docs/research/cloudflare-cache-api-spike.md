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
a factor of twenty**: the window is 10 ms, and most of the 201 ms was this instrument's own
cold sockets and burst queueing. Every claim below that reasons from "~200 ms" is superseded
there.

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
  it opens at 10 ms. See the follow-up section.** The earliest cross-isolate hit was
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

Status: **MEASURED** on 2026-08-04 against a second zone-routed deploy of the same package, at
`probe.ocel.site`, all runs reaching colo `JNB`. Headline: **`W = 10 ms`**, and the first
thing measured was not `W` at all.

The spike above ends with "a follow-up measurement is warranted … a cold-key burst that
measures how many concurrent writers reach `origin()` before the first sentinel becomes
colo-visible." `scripts/race.ts` is that measurement. It races claimers into a key nobody has
written, each running the exact `match`-then-`put` that `admitRefresh` runs, which the
write-then-read runner above structurally cannot do.

### Run metadata

| field | value |
| --- | --- |
| date | 2026-08-04, ~19:2x–20:0x UTC |
| probe host | `probe.ocel.site` (zone route `probe.ocel.site/*`), plus one control run on `probe.ocel.dev`. `ocel.site` was chosen because `ocel.dev` carries the `*.ocel.dev/*` wildcard worker route |
| deploy | the same `ocel-cache-probe` script and the same two committed zone routes as the runs above; no DNS record was created — both zones already carry a proxied `*` AAAA record |
| runner location | one host outside Cloudflare; every request in every run landed in colo `JNB` |
| raw run files | `workers/cache-probe/runs/race-{control-1,control-2,control-3,control-dev,gap-1,gap-2,burst-1,burst-2}.json` (gitignored, on the machine that ran them) |

| run | invocation (all `node scripts/race.ts --base https://probe.ocel.site`) |
| --- | --- |
| control-1..3 | `--phase control --sockets 32` / `--sockets 48` ×2 |
| control-dev | `--phase control --sockets 48` against `https://probe.ocel.dev` |
| gap-1 | `--phase gap --trials 200 --deltas 0,5,10,15,20,25,30,40,50,75,100,150,200,300,500,1000 --sockets 16` |
| gap-2 | `--phase gap --trials 200 --deltas 0,5,10,15,20,25,30,50 --sockets 16` |
| burst-1, burst-2 | `--phase burst --trials 100 --sizes 2,8,32,128 --window 10` |

A deployment note, matching the one recorded above for `ocel.dev`: for the first ~3 minutes
after `wrangler deploy`, ~12 % of requests to `probe.ocel.site` returned `522` — the zone route
had not reached every edge machine and the request fell through to the AAAA record's dummy
origin. It settled on its own; every run below was taken after three consecutive clean bursts
of 60. The runner's own preflight repeats that check with 200 requests and aborts on any
non-200.

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

**Verdict `both-scopes-visible` in all four runs, 254 of 254 cross-isolate reads hit, and
`verified: true` on every write.** An off-zone synthetic key stores and is colo-visible
exactly as an on-zone key is. **The four cache tiers are live in production.** This is a
negative result and it was the one worth being most afraid of; it is recorded first because a
`offzone-inert` verdict here would have superseded this whole issue with a P1 defect.

### 1. `W`, the write-visibility window — 10 ms

Two racers per trial on a **fresh UUID key**, the second fired a driver-imposed Δ after the
first was *sent*. Δ is imposed on the driver's single clock and both racers pay the same round
trip, **so the driver's RTT cancels out of Δ** rather than having to be subtracted from it.
No two Worker clocks are differenced anywhere, preserving the rule the runner above sets.

The statistic is `P(second claimed | second on a different isolate, same colo, first claimed)`.
200 trials per Δ, 3 200 trials in run 1.

| Δ (ms) | run 1 decidable / claims / rate | run 2 decidable / claims / rate |
| --- | --- | --- |
| 0 | 158 / 126 / **0.7975** | 151 / 125 / **0.8278** |
| 5 | 167 / 64 / **0.3832** | 178 / 70 / **0.3933** |
| 10 | 170 / 6 / **0.0353** | 181 / 4 / **0.0221** |
| 15 | 153 / 2 / 0.0131 | 162 / 0 / 0.0000 |
| 20 | 161 / 0 / 0.0000 | 158 / 1 / 0.0063 |
| 25 | 165 / 0 / 0.0000 | 194 / 0 / 0.0000 |
| 30 | 159 / 0 / 0.0000 | 182 / 0 / 0.0000 |
| 40 | 143 / 0 / 0.0000 | — |
| 50 | 157 / 0 / 0.0000 | 175 / 0 / 0.0000 |
| 75 | 176 / 0 / 0.0000 | — |
| 100 | 157 / 0 / 0.0000 | — |
| 150 | 160 / 0 / 0.0000 | — |
| 200 | 159 / 0 / 0.0000 | — |
| 300 | 158 / 0 / 0.0000 | — |
| 500 | 168 / 0 / 0.0000 | — |
| 1000 | 147 / 0 / 0.0000 | — |

**`W = 10 ms`, verdict `measured`, in both runs independently.** Defined as: the elapsed time
after a claimer *begins its `match`* beyond which a second isolate's `match` hits with
probability ≥ 0.95. Deliberately end-to-end over the claim path, so it includes the leader's
`put` duration as well as propagation — a racer arriving during the leader's `put` escapes
too, and any definition anchored on "after `cache.put` returns" would undercount.

Zero trials discarded in either run. Zero `zero-claims` trials and zero `mixed-colo` trials in
6 400 gap trials across both runs — `foreignColoObservations: 0` in the spike above was not a
fluke of that instrument. Trials excluded as `same-isolate` ran 6–57 per Δ (production
collapses those in L0 before the sentinel is consulted, so they are not L1's to measure);
`leader-did-not-claim` totalled 15 in 6 400, nearly all at Δ=0 where "leader" is arbitrary.
The achieved Δ tracked the imposed Δ to within ~1.3 ms at the median at every step.

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

Sequential, on one already-warm socket, so it is a latency and not a queueing delay.

**`201 ms ≈ 10 ms of window + ~65 ms of round trip + ~125 ms of the instrument itself.`** The
residual is not propagation. The spike's sentinel phase stamped `elapsedMs` when a read's
*response returned to the driver*, and its first read burst was 64 concurrent requests on the
default undici pool's **cold** sockets — 64 TLS handshakes and 64-way queueing, both inside
the number. So the 201 ms is an upper bound that is roughly 95 % instrument, and the sentence
in verdict 1 above — "the suppression window opens at ~200 ms, not at 0" — **overstates the
window by a factor of about twenty.** The window opens at 10 ms.

This falsifies prediction P5 (that `W` would come out near `201 ms − RTT`). The two methods
disagree by ~125 ms and the disagreement is fully explained by the older instrument's cold
sockets and burst queueing; the gap sweep is the method that does not contain them, which is
why it is the primary instrument here. Recorded as a disagreement rather than resolved by
picking the convenient number.

### 3. `E(N)`, escapes per colo per stale event

`N` racers on `N` **pre-warmed** sockets at one cold key, 100 trials per `N`. Escapes are
collapsed by isolate, because production runs `refreshOnce` (L0) inside `admitRefresh`, so two
concurrent requests on one isolate never both reach the sentinel; `rawClaims` is shown only to
demonstrate the collapse happened.

| N | run | counted | escapes min / p10 / median / p90 / max | raw claims median | distinct isolates min–max | send dispersion median | verdict |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 2 | 1 | 100 | 1 / 1 / **1** / 1 / 1 | 1 | 2–2 | 0.038 ms | not a lower bound |
| 2 | 2 | 100 | 1 / 2 / **2** / 2 / 2 | 2 | 2–2 | 0.035 ms | not a lower bound |
| 8 | 1 | 100 | 3 / 4 / **5** / 6 / 6 | 7 | 6–6 | 0.066 ms | not a lower bound |
| 8 | 2 | 100 | 4 / 5 / **6** / 7 / 8 | 6 | 8–8 | 0.063 ms | not a lower bound |
| 32 | 1 | 100 | 7 / 8 / **9** / 9 / 11 | 18 | 9–11 | 0.143 ms | not a lower bound |
| 32 | 2 | 100 | 2 / 9 / **11** / 13 / 14 | 17 | 16–16 | 0.147 ms | not a lower bound |
| 128 | 1 | 100 | 14 / 40 / **54** / 68 / 80 | 82 | 70–80 | 0.263 ms | not a lower bound |
| 128 | 2 | 100 | 32 / 46 / **53** / 63 / 67 | 85 | 64–72 | 0.219 ms | not a lower bound |

Zero trials discarded, zero `zero-claims`, zero `mixed-colo`, zero `single-isolate` and zero
invariant violations in all 800 trials across both runs. Send-time dispersion was two orders
of magnitude below `W` at every `N`, so `E(N)` is not printed as a lower bound.

The shape that matters: at `N = 128` arriving effectively simultaneously, **about two thirds of
the isolates the burst touched each escaped** (54 escapes over 80 isolates). L1 is not
suppressing a synchronized herd to one; it suppresses it to roughly the isolate count.

`I_colo`: this run's highest `distinctIsolates` was **80** at `N = 128`, below the spike's 99,
so the lower bound stays at **`I_colo ≥ 99`**. It is still a lower bound and still had not
plateaued — 128 sockets reached at most 80 isolates, so the pool, not the colo, was the limit.

### 4. Sizing L2 — what `ocelhq-wvag.8` should read

```
E = min(1 + λ_colo · W, I_colo)          escapes per colo per stale event
F = C · E                                 L2 fan-in per stale event
R = C · E / refreshSentinelTtlSeconds     sustained L2 request rate
```

`λ_colo`, the arrival rate on one hot route in one colo, is **not measured here and is not
measurable here**: it is a property of the operator's traffic, not of Cloudflare. It is a
**parameter**. Saying so is part of the deliverable — otherwise PR 8 hard-codes a guess.

At `W = 0.010 s`, `I_colo ≥ 99`, `C ≈ 300` colos carrying traffic, `refreshSentinelTtlSeconds = 5`:

| `λ_colo` (rps) | `E` | `F` | `R` (rps) | vs 500 rps/DO | vs 1000 rps/DO |
| --- | --- | --- | --- | --- | --- |
| 1 | 1.01 | 303 | **61** | under | under |
| 10 | 1.10 | 330 | **66** | under | under |
| 100 | 2.00 | 600 | **120** | under | under |
| 1 000 | 11.00 | 3 300 | **660** | **over** | under |

`R = 60 + 0.6 · λ_colo`, so it crosses 500 rps at `λ_colo ≈ 733` and 1 000 rps at
`λ_colo ≈ 1 567` — a route taking ~220 000 rps globally before the first ceiling. **For
smooth traffic, one DO per route survives.**

Two corrections to `ocelhq-wvag.8`'s own arithmetic fall out of this:

- **The baseline is 60 rps, not 30.** `.8` computes "300 colos / sentinel TTL ≈ 30 rps",
  which implies a TTL of 10. PR 7 shipped `refreshSentinelTtlSeconds = 5`
  (`workers/nextjs/src/cache.ts`), so one escape per colo is `300/5 = 60 rps`.
- **`E` is not 1.** It is `1 + λ_colo · W`, and under a *synchronized* herd it is not that
  either.

**The synchronized-herd case is where `.8`'s rejection of sharding needs a second look.** The
linear term assumes arrivals spread over `W`. §3 measured what happens when they are not: 128
simultaneous arrivals produced 54 escapes, bounded by the isolates they touched rather than by
`1 + λW`. Synchronized arrival is not hypothetical for this system — every colo's sentinel
expires one TTL after it was taken, so a route hot everywhere re-admits on a shared schedule.
In that regime `E → I_colo`, and at `I_colo = 99`:

```
R = 300 × 99 / 5 = 5 940 rps
```

which is **6× the 1 000 rps per-DO ceiling, and 12× the conservative 500**. That is the number
`.8` should design against for a synchronized herd, not the 61 rps of the smooth-traffic row.
`I_colo = 99` is itself a lower bound.

### 5. Predictions — which held, which did not

| # | prediction | outcome |
| --- | --- | --- |
| P0 | an off-zone synthetic key stores and is colo-visible identically to an on-zone key | **held** — 254/254, four runs, two zones |
| P1 | second-racer claim probability decreases monotonically with Δ and reaches <0.05 by Δ≈250 ms | **held, and by 25×** — monotone, and <0.05 by Δ=10 ms |
| P2 | at Δ=0 with ≥2 distinct isolates, both racers claim | **held** — 0.80 and 0.83 at Δ=0; and at N=128, 54 escapes over 80 isolates |
| P3 | escapes ≤ distinct isolates in every trial after collapse | **held** — 0 invariant violations in 800 trials |
| P4 | escapes ≥ 1 in every trial | **held** — 0 zero-claim trials in 7 200 trials |
| P5 | `W ≈ 201 ms − median RTT`, within the RTT's own spread | **FALSIFIED** — `W` is 10 ms, not ~132 ms. See §2; the residual is the older instrument's cold sockets and 64-way burst queueing, and the disagreement is reported rather than resolved by choosing |

### 6. Staleness clause

**`W` was measured against a `match`-then-`put` claim with no compare-and-set
(`workers/nextjs/src/cache.ts`, `claimSentinel`) and against `refreshSentinelTtlSeconds = 5`
(same file). If either changes, this number is void.** `workers/cache-probe/src/race.ts`
carries the same warning at the mirror itself.

One comment in `workers/nextjs/src/cache.ts` — the one justifying
`refreshSentinelTtlSeconds = 5` by citing "a sentinel becoming readable from sibling isolates
at ~200 ms" — is now **wrong by a factor of twenty**. The constant it justifies is unaffected
(5 s is still far above 10 ms), only the reasoning is. That edit belongs to `ocelhq-wvag.8`,
not to the probe.

### 7. What these numbers do not cover

- **One colo, one runner.** `W` and `E` are JNB's. `C ≈ 300` is an assumption about
  Cloudflare's fleet, not a measurement taken here.
- **Send-side dispersion only.** The 0.03–0.26 ms dispersion is the spread of the driver's
  *sends*. Server-side arrival spread at `N = 128` is not measured, and it is the most likely
  explanation for why 128 near-simultaneous racers produced 54 escapes rather than 80.
- **`I_colo` is a lower bound and has never plateaued.** 128 sockets reached at most 80
  isolates here; the spike above reached 99 with 200-way concurrency.
- **`λ_colo` is an operator parameter**, restated here because every conclusion in §4 is
  conditional on it.

## Cleanup

This package is a spike. Once the findings above are recorded and children 8 and 9 have
landed against them, `workers/cache-probe` should be deleted; this document is the artefact
that survives. It was `7 and 8` until `ocelhq-wvag.9` re-used the package for the
write-visibility measurement below, which the write-then-read runner above could not take.
