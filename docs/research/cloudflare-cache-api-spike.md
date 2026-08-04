# Cloudflare Cache API spike — cross-isolate visibility, honored TTL, isolate count

Status: **instrument built, UNMEASURED.** No deployed run has happened. Every results
section below is a blank the first real run fills in. Nothing here may be quoted as a
measurement until it is.

Serves `ocelhq-wvag.1` / epic decision 12. Gates `ocelhq-wvag.7` (edge L1 sentinel) and
`ocelhq-wvag.8` (L2 lease Durable Object).

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

> **UNMEASURED.** Fill in from a real deployed run. Do not estimate, interpolate, or infer
> any of these from local runs or from Cloudflare documentation.

### Run metadata

| field | value |
| --- | --- |
| date | UNMEASURED |
| probe host (must not be workers.dev) | UNMEASURED |
| runner location | UNMEASURED |
| runner invocation | UNMEASURED |
| raw run file | UNMEASURED |

### 1. Cross-isolate sentinel visibility

| field | value |
| --- | --- |
| verdict | UNMEASURED |
| writer isolate / colo (from the PUT) | UNMEASURED |
| entry readable from the writing isolate (`verified`) | UNMEASURED |
| distinct reader isolates in the writer's colo | UNMEASURED |
| cross-isolate reads (denominator) | UNMEASURED |
| cross-isolate hits | UNMEASURED |
| **cross-isolate hit rate** | UNMEASURED |
| writer-isolate reads / hits | UNMEASURED |
| first cross-isolate hit after the write | UNMEASURED |
| observations discarded as foreign-colo | UNMEASURED |

The hit rate is the load-bearing number, not the verdict. `2/500` and `500/500` are both
"some sharing" and mean completely different things for L2.

### 2. Honored TTL for `cache.put`

| requested TTL | verdict | last hit | first miss after it | still live at end of window | polls / authoritative polls |
| --- | --- | --- | --- | --- | --- |
| 10s | UNMEASURED | UNMEASURED | UNMEASURED | UNMEASURED | UNMEASURED |

Clock-independent evidence for the same TTL, which does not depend on polling luck:

| requested TTL | max `age` Cloudflare reported | `cache-control` Cloudflare returned | entry readable from the writing isolate |
| --- | --- | --- | --- |
| 10s | UNMEASURED | UNMEASURED | UNMEASURED |

Record at least the 10s case, since that is the TTL already shipped in
`workers/nextjs/src/tag-clock.ts`. Adding `--ttls 1,5,10,30,60` locates a floor if one exists.
A returned `cache-control` that differs from the `max-age=<ttl>` written is a direct answer to
whether `cache.put` rewrites TTLs, independent of every timing measurement here.

### 3. Isolates per colo under load

| colo | distinct isolates | requests | concurrency used | rounds |
| --- | --- | --- | --- | --- |
| UNMEASURED | UNMEASURED | UNMEASURED | UNMEASURED | UNMEASURED |

Treat the isolate count as a lower bound — see the limitations above.

## Verdict on the design

> **UNMEASURED.** Answer these three explicitly once the run above is filled in.

1. **Is L1 viable as designed?**
   - `cross-isolate-visible` — yes, as designed.
   - `partially-visible` — the expected real answer, and the one that needs the most care. L1
     works, but suppresses only the measured fraction. **Child 8's L2 must be sized by the
     observed suppression factor from §1, not by colo count**: the fan-in it absorbs is
     roughly the §3 isolate count scaled by `1 − crossIsolateHitRate`, not one request per
     colo. Quote the rate, never the verdict, when sizing.
   - `isolate-local` — L1 is a no-op: it must be dropped or reduced to a per-isolate memo, and
     **child 8's L2 must be sized for full isolate-count fan-in per colo**, using §3.
   - `never-cached` with `verified: false` — the probe was deployed somewhere the Cache API
     does not work; fix the deployment and re-run rather than concluding anything.
   - `never-cached` with `verified: true` — the cache stored the entry and lost it inside the
     read window; that is a TTL finding, not a visibility one.
2. **Does `snapshotTtlSeconds = 10` hold?** If the TTL is floored upward, the tag-clock's
   stated invalidation bound ("an invalidation reaches a PoP within one TTL") is wrong by the
   difference and the constant, or the claim, must change. If entries are evicted early, the
   front absorbs less read load than assumed — cheaper to be wrong about, but still worth
   correcting in the comment there. Prefer the `age` / `cache-control` evidence over the poll
   bracket where the two disagree: it comes from Cloudflare's clock, not this probe's.
   `indeterminate` with `authoritativePolls: 0` means the run proved nothing about TTL — raise
   `--pollFanout` and re-run; it does **not** mean the TTL was violated.
3. **What fan-in must L2 absorb?** From §3, scaled by the §1 suppression factor.

## Cleanup

This package is a spike. Once the findings above are recorded and children 7 and 8 have
landed against them, `workers/cache-probe` should be deleted; this document is the artefact
that survives.
