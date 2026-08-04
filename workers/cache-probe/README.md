# cache-probe

A throwaway instrument for `ocelhq-wvag.1`. It answers three questions about Cloudflare's
Cache API that Cloudflare does not document, and that the edge L1 sentinel design
(`ocelhq-wvag.7`) and the L2 lease sizing (`ocelhq-wvag.8`) both rest on:

1. Is a `cache.put` written by one isolate readable by **other isolates in the same colo**,
   and after how long?
2. Is a short `max-age` — specifically the `snapshotTtlSeconds = 10` that
   `workers/nextjs/src/tag-clock.ts` already bets on — actually honored?
3. How many isolates does one colo spread a burst across?

Findings go in `docs/research/cloudflare-cache-api-spike.md`. That document is the
deliverable; this package is only how it gets filled in.

## Deploy it on a zone, never on workers.dev

`caches.default` is a **no-op on `*.workers.dev`**: `put()` is accepted and silently
discarded and `match()` never hits. A probe deployed only to workers.dev reports
`never-cached` for every measurement and proves nothing.

Deploy it as a **zone route**, which is how `cloud/edge/cloudflare` attaches a real Ocel app
that has domains — so the measurement is taken through the same routing production uses. Add
it to `wrangler.jsonc` before deploying (there is a commented placeholder):

```jsonc
"routes": [{ "pattern": "probe.<your-zone>/*", "zone_name": "<your-zone>" }]
```

The runner prints a warning if it is pointed at a workers.dev host, and the probe echoes the
`host` it served from into every response so the raw run file records which it was.

## Commands

```bash
cd workers/cache-probe

pnpm install
pnpm typecheck
pnpm test                       # local only; see the caveat below
pnpm build                      # wrangler deploy --dry-run --outdir=dist

pnpm wrangler deploy            # requires the zone route above

node scripts/probe.ts --base https://probe.<your-zone>
```

The run takes a few minutes and writes every raw observation, plus the summaries, to
`runs/probe.json` (gitignored). Paste the summaries into the findings doc, and keep the raw
file if any verdict comes back `indeterminate`.

Repeat the run from a second network location if you want a second colo in the census — a
single runner machine reaches one colo.

Useful flags (defaults in parentheses):

| flag | meaning |
| --- | --- |
| `--base` | probe origin, required |
| `--concurrency` (64) | requests per burst; this is what spreads load across isolates |
| `--rounds` (4) | census rounds, each on a fresh connection pool; the sentinel phase reads for `rounds * 2` bursts |
| `--sentinelSeconds` (60) | TTL for the sentinel entry, long enough not to expire mid-read |
| `--ttls` (10) | comma-separated TTLs to measure, e.g. `--ttls 1,5,10,30` |
| `--pollSeconds` (1) | TTL poll interval; it bounds how tightly the lifetime is bracketed |
| `--pollFanout` (8) | reads per poll, counted as a hit if any read hit |
| `--windowSeconds` (180) | give up waiting for expiry after this long |
| `--out` | where to write the raw run |

## Surface

| route | does |
| --- | --- |
| `GET /identity` | `{ isolate, colo, host }` — the isolate id is module state, minted once per isolate |
| `PUT /entry?run=&ttl=` | `cache.put` a sentinel naming the writing isolate, with `cache-control: max-age=<ttl>`, then reads it straight back from the same isolate and reports that as `verified` |
| `GET /entry?run=` | `cache.match`, reporting `hit`, the sentinel's `writer`, and Cloudflare's `age` and `cache-control` |

Runs are keyed by a unique `run` id, so a repeat run never reads a previous run's entry and
nothing ever needs deleting.

`verified` is the positive control: `false` on a run whose sentinel verdict is `never-cached`
says the cache stored nothing at all, which is a deployment problem rather than a finding.

## Two confounds the runner works around, and one it cannot

- **Socket reuse.** undici pools keep-alive sockets per origin, so repeating a burst on the
  default dispatcher replays the same connections. The census opens a fresh dispatcher per
  round. Whether Cloudflare pins a request to an isolate by connection at all is undocumented
  — this probe is measuring that layer, so it assumes nothing and just varies the input.
- **Polling luck in the TTL phase.** If the cache is isolate-local, only a read landing on the
  writer's isolate can hit, and a fan-out that missed it says nothing about the entry's
  lifetime. The runner therefore records the serving isolate per read and counts only polls
  that could observe the entry (`authoritativePolls`); when none could, the verdict is
  `indeterminate` rather than a confident `evicted-early`.
- **Colo scope.** A single runner machine reaches one colo, and a colo is many machines. Even
  a correct run measures the sharing behaviour of the machines that machine's requests reach.

## What the local test suite does and does not prove

`pnpm test` runs under `@cloudflare/vitest-pool-workers`, whose cache is an in-process store
in a single isolate with no colo. It proves the probe's HTTP contract and the analysis
verdicts, which is why the analysis lives in `src/analysis.ts` behind a real seam. **It
proves nothing about cross-isolate visibility or honored TTL.** Those come only from a real
deploy, which is why the findings doc's results sections stay `UNMEASURED` until one runs.

## Verdicts

Sentinel: `cross-isolate-visible` (≥90% of cross-isolate reads hit; L1 viable as designed) ·
`partially-visible` (some hit, some missed — read `crossIsolateHitRate`, which is the
suppression factor L2 must be sized by) · `isolate-local` (other isolates read and all missed
while the writer hit; L1 is a no-op and L2 must be sized for isolate-count fan-in) ·
`never-cached` (cache inert — check `verified` and the host) · `inconclusive` (every read
landed on the writer's own isolate; raise `--concurrency`).

TTL: `honored` (a hit at or past the requested TTL) · `evicted-early` (gone before it) ·
`indeterminate` (the bracket straddles the TTL — lower `--pollSeconds` — or no poll could
observe the entry at all, i.e. `authoritativePolls` is 0) · `never-cached`.
`stillLiveAtEndOfWindow` on a `honored` verdict means Cloudflare held the entry well past the
requested TTL, which is its own finding — raise `--windowSeconds` to bound it.
`maxObservedAgeSeconds` and `observedCacheControl` are Cloudflare's own account of the entry
and are independent of polling luck: an `age` well past the requested TTL, or a
`cache-control` that differs from what was written, answers the floored-TTL question directly.
