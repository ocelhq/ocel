# cache-probe

A throwaway instrument for `ocelhq-wvag.1`, `ocelhq-wvag.9` and `ocelhq-wvag.16`. It answers questions about
Cloudflare's Cache API that Cloudflare does not document, and that the edge L1 sentinel design
(`ocelhq-wvag.7`) and the L2 lease sizing (`ocelhq-wvag.8`) both rest on.

`scripts/probe.ts` (`.1`) writes once and then reads:

1. Is a `cache.put` written by one isolate readable by **other isolates in the same colo**,
   and after how long?
2. Is a short `max-age` — specifically the `snapshotTtlSeconds = 10` that
   `workers/nextjs/src/tag-clock.ts` already bets on — actually honored?
3. How many isolates does one colo spread a burst across?

`scripts/race.ts` (`.9`) makes requests contend for a cold key, which the first runner cannot
do at all:

4. Does `caches.default` store a key whose hostname is **not on the serving zone**? Every
   colo-cache key in `workers/nextjs` is synthetic — `cache.ocel`, `refresh.ocel`, `isr.ocel`,
   `image.ocel` — and `.1` only ever proved an on-zone key. This gates everything else.
5. How long after a claimer begins its `match` does a second isolate's `match` see it — the
   **write-visibility window `W`**, which is the term that actually sizes L2.
6. How many claims **escape** L1 in one colo per stale event at burst size `N`?
7. How far does a **jittered admission delay** `J` — the one `ocelhq-wvag.16` proposes for L1 —
   drive that escape count down, and does it approach 1 or plateau above it?

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

node scripts/probe.ts --base https://probe.<your-zone>   # ocelhq-wvag.1, the write-then-read
node scripts/race.ts  --base https://probe.<your-zone> --phase control   # ocelhq-wvag.9
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

## The race (`scripts/race.ts`, `ocelhq-wvag.9`)

Three phases, run in order, each a separate invocation.

```bash
node scripts/race.ts --base https://probe.<your-zone> --phase control
node scripts/race.ts --base https://probe.<your-zone> --phase gap   --trials 200
node scripts/race.ts --base https://probe.<your-zone> --phase burst --trials 100 \
  --sizes 2,8,32,128 --window <W from the gap phase, in ms>

# the ocelhq-wvag.16 sweep: one burst size, the jitter window swept
node scripts/race.ts --base https://probe.<your-zone> --phase burst --trials 100 \
  --sizes 128 --window 8 --jitters 0,100,250,500,1000,2000
```

**Phase 0, `--phase control`, gates the other two.** It writes and reads back the same record
twice: once under an on-zone key (`/__control/<run>` on the serving origin, the shape `.1`
proved) and once under the production shape (`https://refresh.ocel/__control/<run>`), each
followed by a fan-out of foreign-isolate reads. Verdicts:

| verdict | means |
| --- | --- |
| `both-scopes-visible` | production's synthetic hostnames store and are colo-visible; proceed |
| `offzone-inert` | **stop.** The on-zone key works and production's shape does not, so L1, the entry tier, the tag-clock front and the image tier are all silently no-ops in production. That is a defect that outranks measuring any window |
| `onzone-inert` | the mirror image: production's shape stores and an on-zone key does not. Nothing in `workers/nextjs` keys on-zone, so this is not a product defect — but it is a finding about `caches.default`, and raising `--sockets` will not move it |
| `cache-inert` | nothing stored anywhere: workers.dev, or the route is not live |
| `inconclusive` | no read reached an isolate other than the writer's; raise `--sockets` |

**Phase 1, `--phase gap`, yields `W`.** Two racers per trial on a fresh key, the second fired
a driver-imposed Δ after the first was *sent*. The statistic is `P(second claimed | second on a
different isolate, same colo, first claimed)`, and `W` is the smallest Δ at which it drops
below 0.05 **and stays there at the next Δ**. Because Δ is imposed on the driver's single
clock and both racers pay the same round trip, **the driver's RTT cancels out of Δ** — that is
the whole reason for the sweep, and why `.1`'s "~200 ms" (which contained the driver's RTT)
could not answer this. The phase also records the driver's own RTT to `/identity`, which is
the number `.1` never wrote down.

Verdicts: `measured` · `below-resolution` (the smallest Δ swept already suppresses, so `W` is
at or under it — at Δ=0 that means the claim is colo-visible at once) · `exceeds-sweep` (still
claiming at the widest Δ; raise `--deltas`) · `inconclusive` (some Δ had too few decidable
trials to be ordered against the others; raise `--trials` or `--sockets`).

**Phase 2, `--phase burst`, yields `E(N)`.** `N` racers on `N` pre-warmed sockets at one cold
key, 100 trials per `N`, drawn as a **rotating window over a pool of `N + --sockets`**, with the
pool itself **re-opened every 25 trials**. Both are load-bearing, not tidiness: a socket pins to
an isolate for the pool's whole life, so a pool of exactly `N` makes 100 trials into 100 repeats
of one isolate combination — and combinations differ enormously, some seeing each other's claim
at once and some not until `W`. A fixed pool reports one draw with no spread and calls it `E(N)`.
Rotating a window over one pool is still one draw of ISOLATES, which is why the pool is redrawn
as well; the window steps by one socket, since a stride of `N` shares a factor with `N + 16` and
visits only a handful of the offsets. Escapes are **collapsed by isolate**, because production runs
`refreshOnce` (L0) inside `admitRefresh`, so two concurrent requests on one isolate never both
reach the sentinel; `rawClaims` is reported alongside only to show the collapse happened.
Trials that spanned colos are discarded and counted; trials that never left one isolate go in
their own bucket (they measured L0, not L1) and never into the headline.

Escapes print as a **lower bound** whenever the racers' send-time dispersion was not small
against `W` — racers that were not really concurrent under-report escapes, and "L1 suppresses
beautifully" is exactly the comfortable answer that failure mode manufactures. Dispersion is
taken from undici's `undici:client:sendHeaders` channel, i.e. the moment each racer's first
byte is written to its socket. It is **not** taken from the driver's own dispatch loop: calling
`request()` does not send, so those timestamps are microseconds apart however long the sends
really take, and a guard built on them can never fire.

Three of the burst's four buckets are **structurally unreachable and prove nothing by coming
back zero**, which is recorded here so no run cites their zeros as validation. `zero-claims`
cannot fire because the globally first `match` on a fresh UUID key must miss and therefore
claim; `mixed-colo` cannot fire from a single driver host, which reaches exactly one colo; and
`single-isolate` is live but near-vacuous at `N ≥ 2` on distinct pre-warmed sockets. The gap
phase's `leader-did-not-claim` and `same-isolate` buckets are the ones that demonstrably fire.

**`--jitters` sweeps the admission delay (`ocelhq-wvag.16`).** Each racer's worker draws
`U[0, J)` and sleeps it *before* claiming, so the delay sits on the same side of the network as
the claim. A driver-imposed delay would also spread the **arrivals**, and arrival spread
suppresses claims on its own — the effect this instrument has to hold constant. `J = 0`
reproduces the un-jittered burst exactly, and is the baseline every jittered row is read
against.

Two live checks guard it, because "the flag was accepted" is not evidence the flag was used:

- **the draws must cover the window.** Each racer echoes the delay it drew, and a run whose
  draws do not straddle `J/2` aborts. A worker that ignored `--jitter` would otherwise print a
  collapsed herd for a system that never jittered — the same shape of dead detector as the
  fixed socket pool that voided the first burst.
- **the draw must fit inside the request.** The driver times each racer from before the
  request is queued, which bounds the worker's own wall clock from above, so a delay reported
  but not slept aborts the run.

`lateEscapes` is what tells a collapse from a floor. It counts, per trial, the escaping
isolates whose draw put them more than `W` **after the trial's first claim** — measured against
the first claim rather than the nearest one before it, because the first has had the longest to
propagate and is therefore the most generous explanation available. If `E` stops falling as `J`
grows and `lateEscapes ≈ E − 1`, the floor is isolates that cannot see each other's claims at
any separation (`.9` §7 measured that non-uniformity directly). If `lateEscapes ≈ 0`, the
window simply has not been outrun yet.

With `--window` and no jitter, the phase prints the sizing table PR 8 cites:

```
E = min(1 + λ_colo · W, I_colo)      escapes per colo per stale event
F = C · E                            L2 fan-in per stale event
R = C · E / refreshSentinelTtlSeconds sustained L2 request rate
```

`λ_colo` — the arrival rate on one hot route in one colo — is **not measured here and is not
measurable here.** It is a property of the operator's traffic, not of Cloudflare, so it is a
parameter and the table is printed across a range of it.

The table is **suppressed on a jittered sweep**: `E = 1 + λ·W` models the un-jittered path,
where every arrival inside the window claims. Under jitter the claimant pool inside one window
is bounded by the isolate count rather than by λ (`1 + I_colo·W/J`, which takes no λ at all),
and printing the un-jittered model over jittered measurements is exactly the kind of green
number this package exists not to produce.

Flags (defaults in parentheses):

| flag | meaning |
| --- | --- |
| `--base` | probe origin, required |
| `--phase` (`control`) | `control`, `gap` or `burst` |
| `--trials` (200 gap / 100 burst) | trials per Δ or per `N` |
| `--deltas` (`0,10,25,50,100,150,200,300,500,1000`) | the Δ sweep, in ms |
| `--sizes` (`2,8,32,128`) | burst sizes |
| `--jitters` (`0`) | admission-jitter windows to sweep, in ms; `0` is the un-jittered baseline. Every `--sizes` × `--jitters` cell is a full run of `--trials` |
| `--sockets` (16) | pre-warmed connections the gap phase draws its pairs from; minimum 2, since a pool of one can never produce a cross-isolate pair |
| `--scope` (`offzone`) | which key shape to race under; `offzone` is production's |
| `--window` | `W` in ms, so the burst phase can print the sizing table |
| `--colos` (300) / `--sentinelTtl` (5) | the other two sizing inputs |
| `--isolates` | an `I_colo` lower bound inherited from another run, if it is higher than this one's; the sizing table caps `E` at the larger of the two |
| `--maxDiscardRate` (0.02) | abort once this fraction of trials has been discarded on transport failures |
| `--out` | where to write the raw run |

**Every gate is fatal, not a warning.** The run refuses to start on a workers.dev host, on a
control that is not `both-scopes-visible`, or on a preflight that saw an absent colo or two of
them; it aborts on a trial where a racer was answered for another racer's key or without a
colo, because that means the instrument is broken rather than the cache. Nothing is ever
retried: a resend against a key the first send just claimed would report `claimed: false` and
manufacture a suppression that never happened.

Any non-200, redirect or network error **discards the whole trial and is counted** — and above
`--maxDiscardRate` (2% by default) the run aborts rather than printing a window over whatever
survived. Discards are not free: each one rebuilds the socket pool, so a run full of them is
resampling its isolates as it goes. Every summary carries `attempted` alongside `trials` so a
shrinking denominator is visible rather than silent.

## Surface

| route | does |
| --- | --- |
| `GET /identity` | `{ isolate, colo, host }` — the isolate id is module state, minted once per isolate |
| `PUT /entry?run=&ttl=` | `cache.put` a sentinel naming the writing isolate, with `cache-control: max-age=<ttl>`, then reads it straight back from the same isolate and reports that as `verified` |
| `GET /entry?run=` | `cache.match`, reporting `hit`, the sentinel's `writer`, and Cloudflare's `age` and `cache-control` |
| `POST /race?key=&seq=&scope=&ttl=&jitter=` | draws `U[0, jitter)`, sleeps it, then makes one `match`-then-`put` claim on a cold key, reporting `{ claimed, isolate, colo, key, scope, seq, delayMs }`. POST so nothing upstream treats a claim as retriable; the cache key itself is a GET `Request`, since `cache.put` throws on anything else |
| `GET /control?run=&scope=&mode=write\|read` | Phase 0. `write` puts a record and reads it back from the same isolate (`verified`); `read` reports `hit` and the writing isolate |

`/race` and `/control` answer `cache-control: no-store` on a per-racer unique URL, because the
zone's own edge cache sits in front of this worker and could otherwise serve one racer's body
to another and manufacture a duplicate claim.

`src/race.ts`'s claim is a **deliberate mirror** of `claimSentinel` in
`workers/nextjs/src/cache.ts`: `match`, and on a miss `put` — no compare-and-set, every error
admits. If `claimSentinel` gains a compare-and-set, `W` as measured here is void.

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
in a single isolate with no colo, and it stores **any** hostname happily — so a hit on an
off-zone key there is evidence the wiring is right and is emphatically not an answer to the
question Phase 0 exists to ask. It proves the probe's HTTP contract and the analysis verdicts,
which is why the analysis lives in `src/analysis.ts` and `src/race-analysis.ts` behind real
seams. **It proves nothing about cross-isolate visibility, honored TTL, off-zone key storage
or the write-visibility window.** Those come only from a real
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
