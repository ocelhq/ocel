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
| `--rounds` (4) | census rounds; the sentinel phase reads for `rounds * 2` bursts |
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
| `PUT /entry?run=&ttl=` | `cache.put` a sentinel naming the writing isolate, with `cache-control: max-age=<ttl>` |
| `GET /entry?run=` | `cache.match`, reporting `hit`, the sentinel's `writer`, and Cloudflare's `age` |
| `DELETE /entry?run=` | `cache.delete`, for cleaning a colo between runs |

Runs are keyed by a unique `run` id, so a repeat run never reads a previous run's entry.

## What the local test suite does and does not prove

`pnpm test` runs under `@cloudflare/vitest-pool-workers`, whose cache is an in-process store
in a single isolate with no colo. It proves the probe's HTTP contract and the analysis
verdicts, which is why the analysis lives in `src/analysis.ts` behind a real seam. **It
proves nothing about cross-isolate visibility or honored TTL.** Those come only from a real
deploy, which is why the findings doc's results sections stay `UNMEASURED` until one runs.

## Verdicts

Sentinel: `cross-isolate-visible` (L1 viable as designed) · `isolate-local` (L1 is a no-op;
L2 must be sized for isolate-count fan-in) · `never-cached` (cache inert — check the host) ·
`inconclusive` (every read landed on the writer's own isolate; raise `--concurrency`).

TTL: `honored` (a hit at or past the requested TTL) · `evicted-early` (gone before it) ·
`indeterminate` (the bracket straddles the TTL; lower `--pollSeconds`) · `never-cached`.
`stillLiveAtEndOfWindow` on a `honored` verdict means Cloudflare held the entry well past the
requested TTL, which is its own finding — raise `--windowSeconds` to bound it.
