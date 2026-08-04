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
  isolate-count fan-in rather than colo-count fan-in — a design change to child 8.
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
- `PUT /entry?run=&ttl=` does one `cache.put` of a sentinel naming the writing isolate.
  `GET /entry?run=` does one `cache.match` and reports whether it hit and which isolate wrote
  what it found.
- All elapsed times are measured by the runner against its own clock. No two Worker clocks
  are ever differenced: `Date.now()` inside a Worker advances only on I/O, so cross-isolate
  arithmetic on it would measure the runtime, not the cache.
- Verdicts are computed by `src/analysis.ts`, which is unit-tested, and are deliberately
  conservative: a run that cannot distinguish two outcomes reports `inconclusive` or
  `indeterminate` rather than guessing.

### What has been validated, and what that is worth

Locally: `pnpm typecheck`, `pnpm build` (`wrangler deploy --dry-run`) and `pnpm test` all
pass, and the runner was driven end-to-end against `wrangler dev`. That run reported
`1 isolate`, sentinel `inconclusive`, TTL `indeterminate` — exactly what a single-isolate,
single-process, colo-less local runtime should report.

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
| writer isolate / colo | UNMEASURED |
| distinct reader isolates in the writer's colo | UNMEASURED |
| cross-isolate hits | UNMEASURED |
| first cross-isolate hit after the write | UNMEASURED |

### 2. Honored TTL for `cache.put`

| requested TTL | last hit | first miss after it | verdict | still live at end of window |
| --- | --- | --- | --- | --- |
| 10s | UNMEASURED | UNMEASURED | UNMEASURED | UNMEASURED |

Record at least the 10s case, since that is the TTL already shipped in
`workers/nextjs/src/tag-clock.ts`. Adding `--ttls 1,5,10,30,60` locates a floor if one exists.

### 3. Isolates per colo under load

| colo | distinct isolates | requests | concurrency used |
| --- | --- | --- | --- |
| UNMEASURED | UNMEASURED | UNMEASURED | UNMEASURED |

## Verdict on the design

> **UNMEASURED.** Answer these three explicitly once the run above is filled in.

1. **Is L1 viable as designed?** `cross-isolate-visible` means yes. `isolate-local` means L1
   is a no-op: it must be dropped or reduced to a per-isolate memo, and — this is the
   design-changing consequence the issue calls out — **child 8's L2 must be sized for
   isolate-count fan-in per colo, not one request per colo**, using the number measured in §3.
   `never-cached` means the probe was deployed somewhere the Cache API does not work; fix the
   deployment and re-run rather than concluding anything.
2. **Does `snapshotTtlSeconds = 10` hold?** If the TTL is floored upward, the tag-clock's
   stated invalidation bound ("an invalidation reaches a PoP within one TTL") is wrong by the
   difference and the constant, or the claim, must change. If entries are evicted early, the
   front absorbs less read load than assumed — cheaper to be wrong about, but still worth
   correcting in the comment there.
3. **What fan-in must L2 absorb?** From §3, and from whether L1 collapses it.

## Cleanup

This package is a spike. Once the findings above are recorded and children 7 and 8 have
landed against them, `workers/cache-probe` should be deleted; this document is the artefact
that survives.
