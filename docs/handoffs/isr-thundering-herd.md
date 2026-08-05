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

Every tip below was read off `git log --oneline` per branch and every parent edge verified with
`git merge-base --is-ancestor` on 2026-08-05. A prior handoff carried stale shas through a
restack; transcribing this diagram instead of re-deriving it is how that happens.

```
main
 └─ 1  isr-herd/01-cache-api-spike        6dd5313  ocelhq-wvag.1  ✅ CLOSED (measured)
     └─ 2  isr-herd/02-isr-writer          39c5668  ocelhq-wvag.2  ✅ CLOSED, reviewed ×2, not pushed
         └─ 3  isr-herd/03-manifest-projection aef588a ocelhq-wvag.3 ✅ CLOSED, reviewed, not pushed
             └─ 4  isr-herd/04-streams-publisher c887a83 ocelhq-wvag.4 ✅ CLOSED, reviewed, not pushed
                 └─ 5  isr-herd/05-origin-reads-snapshot  dcd4815  ocelhq-wvag.5  ✅ CLOSED
                     └─ 6a isr-herd/06a-publisher-alarms  3fb0c73  ocelhq-wvag.13 ✅ CLOSED
                         └─ 6  isr-herd/06-get-drops-batchget 776d806 ocelhq-wvag.6 ✅ CLOSED (gate cleared)
                             └─ 7  isr-herd/07-edge-l0-l1  faac969  ocelhq-wvag.7  ✅ CLOSED
                                 └─ 9  isr-herd/09-write-visibility db25a0f ocelhq-wvag.9 ✅ CLOSED (measured, then re-measured)
                                     ├─ 10 isr-herd/10-admission-jitter 063ce84+ ocelhq-wvag.16 ✅ CLOSED (measured, built, reviewed ×2, fixed)
                                     │   └─ 8  edge L2 lease   (unstarted) ocelhq-wvag.8  ← unblocked
                                     └─ 17 multi-region herd harness (unstarted) ocelhq-wvag.17  ⛔ on .8
```

`+` means "plus this handoff's own commit", which is the tip of 10 as you read this.

**The stack was restacked into dependency order on 2026-08-04, and every commit sha on branch
07 changed.** Any note elsewhere in this document that cites a sha on 07 was written before the
rebase; the old shape had PR 7 rooted directly on PR 5, jumping the then-blocked `.6`, with the
between-PR work piled on top of 07. Each tip above was verified green after the move.

- **`isr-herd/06a-publisher-alarms` is new** and the old diagram does not name it. `ocelhq-wvag.13`
  is a bd dependency *of* `.6`, so its alarms must sit **below** PR 6; they had been committed
  above PR 7 by mistake. Two commits: `934da28` (the three alarms) and `3fb0c73` (the e2e harness).
- **`.12` and `.14`'s commits stay on 07** as between-PR commits, as they always were. Note
  `d9cc24b` (the fix to `.14`'s own stale comment) **must stay behind `fdf045c`** — it rewrites
  the comment block that commit introduces.
- **Nothing is pushed and `gh stack` reports these branches are NOT tracked.** The stack is still
  managed by hand; do not assume a `gt`/`gh stack` restack will do the right thing with it.

Filed out of band: `ocelhq-wvag.9` (measure the L1 write-visibility window; **blocks `.8`**),
`ocelhq-wvag.10` (live e2e for the writer), `ocelhq-wvag.11` (destroy leaves per-build writer
DO instances behind), `ocelhq-wvag.12` (collapse the twice-derived projection key and
filename into `@ocel/next-cache` — **done**, and it needed no dist build; see "Current
position"). Outside the epic,
`ocelhq-uroj` (the edge user's account-global `Query`/`BatchGetItem` grants, dead before this
stack began; see PR 5).

**`.6`'s gates were `.13`, `.14` and `.15`, then `ocelhq-yo9b` replaced them, and every one of
them is now closed** — `.13`'s alarms ship on `06a`, `.14`'s pin on 07, `.15` proved the
publisher live, and `yo9b`'s membrane pin (`776d806`) is the tip of `06` itself. `.6` has no
open gate left. See "Current position".

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

**`pnpm -r --no-bail typecheck` has exactly ONE failure, and it is not a type error.**
Every per-PR record below used to say "clean except the four pre-existing `examples/*`
packages"; that claim was stale and each one now points here instead. The single failure
is **`examples/next-cache-lab`**, and the directory contains only `package.json` and
`node_modules`: its sources are untracked and gone. `tsc --noEmit` finds nothing to check,
prints its help text and exits 1. It is unrelated to the dogfooded-SDK build failure that
causes the `pnpm -r test` failures, and no change in this stack can fix it.

**Writer retirement is bounded, not instant (PR 2).** The writer worker memoizes each
deploy's secret hash per isolate. `destroy` clears the memo only in the isolate that served
it, so an isolate that never handled the retirement keeps authorizing that build's writes
until its memo lapses — up to `MEMO_TTL_MS` (60s) in `workers/isr-writer/src/index.ts`. That
is the accepted bound, not an oversight: closing it means consulting the Durable Object on
every entry write, which is what the memo exists to avoid, and epic decision 6c mandates the
memo. Read decision 6d and commit `79900d5`'s message with that bound in mind — both are
worded as though retirement takes effect everywhere at once, and it does not.

## Current position

**Nothing in this stack is gated any more.** `ocelhq-yo9b` is closed and proven live, so `.6`
closed with it; `.9` and `.16` are measured, built and reviewed. `ocelhq-wvag.8` is the next
thing to build, and `ocelhq-wvag.17` after it.

**Read the margin before designing `.8`.** The jitter took the synchronized fan-in from
`F ≈ 20 000-23 000` requests per stale event to **423-438 — and that is a RATE**, an
instantaneous 423-438 rps at the route's single Durable Object, because it is spread over
exactly `J = 1 s`. Against Cloudflare's conservative 500 rps that is a margin of **~1.2×, not
the ~6× the 85-88 rps sustained figure beside it suggests**, and `C ≈ 300` is an assumption:
the burst crosses 500 rps at `C ≈ 340-355` and 1 000 rps at `C ≈ 685-710`. `.8` carries this as
an acceptance criterion. See "`ocelhq-wvag.16`" below.

### `ocelhq-wvag.16` — the herd was self-inflicted, and jitter removes it — **DONE**

Branch `isr-herd/10-admission-jitter`, rooted on `db25a0f` (tip of 09). **Nine commits**, the
ninth being this handoff edit; last content commit `063ce84`. Nothing pushed. Findings in `docs/research/cloudflare-cache-api-spike.md`, section
"Follow-up: the admission-jitter sweep"; the code is `admissionJitterMs` in
`workers/nextjs/src/cache.ts`. A third `ocel-cache-probe` deploy was taken and **is torn down**,
verified via the API: the script is absent from the account's script list and
`probe.ocel.dev/*` / `probe.ocel.site/*` are absent from both zones' routes.

**`.8` was sized against the wrong constraint, and this is the thing to carry forward.** The
`.9` section below reasons about a *sustained* rate. The binding constraint is the **burst**: a
route goes stale at one wall-clock instant, every colo sees it simultaneously, and
`F = C·E ≈ 20 000-23 000` requests arrive within a few hundred milliseconds against an object
Cloudflare rates at ~500-1 000 rps. That is ~20 s of queueing — overload, not slow success.

**And the synchronization is the system's own doing.** `claimSentinel` fires the instant a
request observes staleness and `settleSentinel` re-arms exactly one TTL later, colo-wide.
Nothing requires the attempts to be simultaneous. So the fix is neither to shard (splitting `k`
ways yields `k` origin renders, restoring the herd) nor to raise the TTL (which moves the
sustained rate and leaves the burst): it is to wait a uniform draw from `[0, 1000 ms)` before
claiming.

Measured before a line of `workers/nextjs` was written, because falsifying it would have
changed `.8`'s shape to a hierarchical per-`(route,colo)` tier. Two runs, 600 trials each, zero
discarded: **`E(128)` mean 1.41 and 1.46 at `J = 1000 ms`, against 54.79 and 61.98 at `J = 0`**.
The gate was 3.

- **The sweep was a sweep, not a single point, and that was the whole design.** `.9` found
  cross-isolate visibility is not uniform inside a colo, so `E` could have floored at
  `p · I_colo` however wide `J` grew. Means across `J = 0, 100, 250, 500, 1000, 2000`:
  54.79/61.98, 3.81/3.79, 2.52/2.31, 1.69/2.05, 1.41/1.46, 1.21/1.26. **No plateau** — still
  falling at 2 000. `.8` needs no hierarchical fallback.
- **The floor is bounded, not waved away.** `lateEscapes` counts escapes whose draw put them
  more than `W` after the trial's *first* claim, which is the claim that has had longest to
  propagate. At `J = 2000` it means 0.03 and 0.07, so any mutually-blind population sits below
  0.07 escapes per colo per stale event. Its zero at `J = 0` is structurally unreachable and is
  labelled in the doc rather than cited.
- **The constant is λ-free by construction.** `J ≥ I_colo·W`, because L0 already collapses each
  isolate to one in-flight admission and *holds that entry across the wait*, so the claimant
  pool inside one window cannot exceed the isolate count. `λ_colo` never enters. The bound
  over-predicts at every `J` (1.8× at 100 ms, 1.05× at 2 000 ms), so choosing from it is
  choosing conservatively.
- **What `.8` should now size against, stated as a rate.** `F = C·E ≈ 423-438` per stale event
  spread over `J = 1 s` **is 423-438 rps** at one Durable Object; `R ≈ 85-88 rps` is the
  sustained figure and is not the constraint. The margin against Cloudflare's conservative
  500 rps is ~1.2×, and the rating is for "simple operations" — a lease persisting state per
  claim is not obviously one. `C ≈ 300` is an assumption: 500 rps at `C ≈ 340-355`, 1 000 rps at
  `C ≈ 685-710`. **One DO per route holds, conditional on the jitter staying in place and on
  the lease failing open under overload** — `.8` names `.16` as the condition and now carries
  the margin as an acceptance criterion.
- **Which `F` the "before" figure is, decided rather than drifted into.** `300 ×` the directly
  measured `E(128)` is `16 437-18 594`; the doc had rounded that outward to "17 000-19 000" and
  quietly substituted it for the extrapolated `20 000-23 000` bd already held. The extrapolated
  one is the defensible one and it is what stands everywhere now: this burst reached 77-86
  isolates against `I_colo ≥ 99`, which has never plateaued, so `300 × E(128)` is a **floor**,
  not an estimate. The propagation therefore ran doc → bd's number, not bd → the doc's.
- **The wait is on `admitRefresh` only.** The miss-path fill runs `refreshOnce` directly, on the
  serving path with joiners awaiting it; a wait there is up to a second of user-visible latency
  on every cold miss. The test that catches it has to *join* a second request to the leader's
  fill — a single request never awaits the fill and passes either way.
- **The production default lives in `cache.ts`, not at `src/index.ts`'s deps construction.**
  Deliberate, and it survived review: nothing wires it, so there is no site at which a test
  double *can* be left behind. `CacheDeps.admissionDelay` exists only as the test seam, and one
  test leaves the default in place and asserts it is drawn, non-zero and bounded. The cost of
  that choice is that the suite gets the real wait unless it says otherwise — see the review
  section below, and `test/cache-deps.ts`, which is now the only place a `CacheDeps` is built.
- **`inFlight` is keyed on `deps.cache`, not on the deps object.** Non-obvious and load-bearing
  for `.8`'s wiring: spreading `CacheDeps` to add a delay does not fragment L0. Asserted by a
  test, and the assertion was confirmed to fail against a `WeakMap` keyed on `deps`.

Every test above was mutation-checked — the production line broken, the named test confirmed
failing, the line restored. Probe-side likewise: a worker that ignores `--jitter` and one that
reports a delay it never slept are both caught by live checks, confirmed against a deliberately
broken worker before the runs were taken.

#### Two reviews ran on this branch, and what they moved

- **The draw is now capped by the entry's remaining stale window** (`9568777`). A route whose
  stale window is shorter than `J` lost L1 *entirely* for the tail of the wait: past
  `expiration` the colo tier returns null before it ever reaches `admitRefresh`, and the request
  falls to the miss path, whose only dedupe is the per-isolate `refreshOnce`. On
  `cacheLife({ revalidate: 1, expire: 2 })` a 700 ms draw plus a 400 ms render leaves 100 ms in
  which every isolate in the colo renders for itself — the herd, reintroduced at exactly the
  boundary the wait pushed the refresh across, and silently. The draw is now uniform over
  `[0, min(J, remaining stale window))`, which is `J` on any normal route and degrades the
  pathological one to un-jittered rather than to worse. The bound travels to all three admission
  sites; `intercept` reports `staleForMs` beside `stale` for the two the R2 tier owns.
- **The burst rate was understated by presentation** and is now a rate, with a `C`-sensitivity
  table. Above, and in the doc's §11.
- **The probe's `lowerBound` guard read dispersion's MEDIAN**, so the `J = 2000` rows — median
  1.51 ms, p90 115 ms, fourteen times `W` in at least a tenth of trials — printed clean, and the
  console line only ever showed the median. Found by reading raw JSON. Now on the p90, with the
  p90 printed. **This is the same shape as the dead `dispersionMs` it replaced.**
- **The burst's "rotating window" had period nine.** `gcd(128, 144) = 16`, so
  `(trial · size) % poolSize` took nine distinct offsets and any two windows overlapped in ≥112
  of 144 sockets — and the pool was opened once per cell, so a cell's isolate population was one
  draw. Materially better than the fixed pool that caused `.9`'s defect (the jitter draws are
  fresh per racer per trial, which is what carries the sweep), but not what the word implies.
  The stride is one now and the pool is redrawn every 25 trials. **The recorded numbers were NOT
  re-taken against that**, and §12 says so: nothing above depends on a difference smaller than
  the 13% the two runs already differ by at `J = 0`.
- **The suite was sleeping the real jitter.** Eighteen inline `CacheDeps` meant every background
  refresh a test drove slept a real `U[0, 1000)` draw — ~2.5 s expected, up to ~5 s worst case,
  redrawn every run, with five tests inside 4× of vitest's default timeout. One constructor
  (`test/cache-deps.ts`) now builds them all; `workers/nextjs` test time fell from 6.71 s to
  2.84 s and stopped varying.
- **Not disputed but worth recording as corrected:** the review placed "wrong by a factor of
  twenty" in `.16`'s description. It is not there — it is in `.8`'s 2026-08-04 19:58 comment,
  in the same sentence as the stale "10 ms". bd comments cannot be edited, so both are corrected
  by a later comment on `.8` rather than in place.

**`.8` was split three ways** (decisions recorded as comments on the epic): this issue took the
jitter and blocks `.8`; `ocelhq-wvag.17` took the load/herd harness that epic decision 15 had
put in `.8`, because it measures the whole stack rather than the lease and needs a multi-region
driver; `.8` keeps the lease DO, wire protocol, Go provisioning and edge wiring. `.8`'s
description has been corrected: it previously concluded the smooth-traffic case holds and asked
the implementer to show why the synchronized regime cannot arise. It does arise, and the burst
figure it omitted entirely is the constraint.

### `ocelhq-wvag.9` — the write-visibility window is 8 ms, and PR 1's 201 ms never contained it — **DONE**

Branch `isr-herd/09-write-visibility`, rooted on `faac969` (tip of 07). **Ten commits**, the
tenth being this handoff edit; last content commit `85f73da`. Nothing pushed. Findings in `docs/research/cloudflare-cache-api-spike.md`, section
"Follow-up: the L1 write-visibility window"; the instrument is `workers/cache-probe/scripts/race.ts`
plus `src/race.ts`, `src/race-analysis.ts` and `src/race-options.ts`. **Two deploys of
`ocel-cache-probe` were taken and both are torn down**, verified via the API each time: the
script is absent from the account's list and `probe.ocel.dev/*` / `probe.ocel.site/*` are absent
from both zones' routes.

**The first pass's numbers were reviewed, re-measured, and two of them moved.** Read the doc's
new §8 for the full account. What did not move: the key-scope control, the gap sweep's design,
and the conclusion that a synchronized herd exceeds one DO per route by an order of magnitude.

Four things to carry forward:

- **Every colo-cache key in `workers/nextjs` is on a synthetic hostname that is on no zone**
  (`cache.ocel`, `refresh.ocel`, `isr.ocel`, `image.ocel`) and nothing had ever tested whether
  `caches.default` stores such a key. PR 1 only proved an *on-zone* key, and Miniflare stores
  any hostname happily. It does store them — 340/340 cross-isolate reads, five runs, two zones.
  All four tiers are live. This gate ran first because a failure would have superseded the
  issue with a P1 defect; it is worth knowing it was checked.
- **`W = 8 ms`, not 10 and not ~200 ms.** The first sweep stepped 0, 5, 10, so the drop sat in a
  5 ms bin and its upper edge was reported as a point value; a 1 ms sweep across that bin, 250
  trials per Δ, twice, gives `measured` at 8 in both runs. **PR 1's 201 ms does not contain the
  window at all** — PR 1 started its clock when the `PUT` *returned to the driver*, a full round
  trip after the write executed at the edge, so the window had already elapsed twice over before
  that clock started, and the old `10 + 65 + 125` decomposition double-counted it. Cold sockets
  and 64-way queueing are the most likely explanation for the residual and that is **untested**.
  The comment in `workers/nextjs/src/cache.ts` justifying `refreshSentinelTtlSeconds = 5` by
  citing "~200 ms" is wrong by a factor of twenty-five; the constant is not. **That edit was
  made under `.16`, not PR 8**, since `.16` is where the same file gained `admissionJitterMs`,
  which is derived from the 8 ms and carries the same staleness clause.
- **`.8`'s sizing arithmetic was wrong twice and its description is corrected.** "300 colos /
  sentinel TTL ≈ 30 rps" assumed a TTL of 10 (PR 7 shipped 5) and one escape per colo. Real
  smooth-traffic sizing is `R = 60 + 0.48·λ_colo`, holding one DO per route to λ ≈ 917 rps per
  colo. **It does not hold for a synchronized herd**, and every colo's sentinel expires exactly
  one TTL after it was taken, so colo-wide synchronization is the system's own default. But do
  **not** derive that regime's number the way this page previously invited: measured `E/I` at
  N=128 is **0.69–0.78 across four runs, never 1.00**. `E → I_colo` is a worst-case bound no run
  reproduced. Applying the measured ratio to `I_colo ≥ 99` gives `E ≈ 68–77`, `R ≈ 4 100–4 640
  rps`; the `E = I_colo` bound gives 5 940. Both are floors. PR 8 must label which row it sizes
  against — the conclusion (an order of magnitude over the ceiling) is the same either way.
- **Cross-isolate visibility is NOT uniform inside a colo, and this is new.** Found while
  diagnosing the burst defect: one pair of sockets had *both* racers claiming 31 of 40 times at
  zero separation, while another pair had the same socket win 79 of 80 **whichever was
  dispatched first** — the winner was a property of the connection, not of the race. `W = 8 ms`
  is a population statistic over isolate pairs, not a property every pair has. Anything in PR 8
  reasoning about a *specific* pair of isolates is outside what was measured.

**Two review rounds landed on this branch and both found dead detectors, the same class as every
other finding in this stack.** `dispersionMs` — the burst's only concurrency guard — was stamped
in the driver's dispatch loop before undici had written a byte, so it read 0.03–0.26 ms and the
guard was false in every real run; it now comes from `undici:client:sendHeaders` and reads
0.06–1.22 ms. `invariantViolations` was true by construction of its only caller and its
certifying test hand-built inputs the driver cannot produce — the `ocelhq-yo9b` pattern verbatim
— so it is deleted, and the live duplicate detector (`outcomeOf`'s echo check, which never
fired) moved into `src/` where the suite reaches it. **The burst also sampled one socket pool per
size, so 100 trials were one draw repeated**; the pool now rotates, and the burst and the gap
sweep cross-validate at `N = 2` for the first time (1.60/1.73/1.82/1.83 against an independently
measured 1.75–1.83).

Verified: `workers/cache-probe` 95/95 tests, `pnpm typecheck` and `pnpm build` clean on the
package; `pnpm -r --no-bail typecheck` clean except `examples/next-cache-lab` (see "Standing
notes"). Fourteen mutations applied, each caught by the named test.

### `ocelhq-yo9b` — the fleet's membrane layer predated PR 2 — **CLOSED, PROVEN LIVE**

**The pin is bumped to `ocel-membrane:22` and the whole chain was re-driven on a real deploy.**
A deployed function's `revalidateTag` wrote a `TAG#` record, which drove a **fresh** publisher
invocation (a cold start two seconds after the raise, in the publisher's own log group) into
**both** stores. The pin is commit `776d806`, which is the tip of `isr-herd/06-get-drops-batchget`
— it has to sit at or below what it gates, and what it gates is PRs 2-5's Lambda-side work.

**R2 was read directly through the Cloudflare API, not inferred from a status code, and that is
the trap.** `workers/isr-writer/src/index.ts` returns **204 for the `"absent"` outcome as well as
for a real publish**, so a 204 cannot distinguish "landed" from "there was no snapshot to land
in". The R2 object was fetched and its records compared byte for byte against the S3 copy.

**The trap that nearly produced a false negative, and the rule that comes out of it:
`make provider` before any deploy that is meant to verify a pin bump.** The prebuilt provider
binary at `packages/native-lib/provider-aws-linux-x64/bin/deploy` predated the pin commit and
still had `:21` baked in, so the first verification deploy attached the old layer *from a tree
that contained the fix*. Editing `cloud/aws/deploy/function.go` does not change what `ocel
deploy` runs. Rebuild (`make provider`, then `make cli lib`) or the deploy is testing the last
build, silently.

The original report, kept as the standing record:

**P1 BUG, blocked `ocelhq-wvag.6` and `ocelhq-wvag.10`.** Deployed
functions attach `ocel-membrane` **v21**, pinned as `defaultMembraneLayerARN` in
`cloud/aws/deploy/function.go`, and that layer predates `ocelhq-wvag.2`. So a route handler's
`revalidateTag` throws `OCEL_ISR_STORE_REGION is not set` — **after** the response is piped, so
the caller sees a 200, no `TAG#` record is written, and the publisher is never invoked.

**Every Lambda-side change in PRs 2, 3, 4 and 5 is therefore unverified on a real deploy.** This
was invisible until a live deploy was driven, because the deploy renders the *new* env surface
regardless of what the layer inside the function reads. Confirmed by building the current layer
and diffing the env surface, not by reasoning: nothing anywhere in the repo reads
`OCEL_ISR_STORE_REGION`, and the current dist reads `OCEL_ISR_WRITER_URL` /
`OCEL_ISR_WRITER_SECRET` — which is exactly what the deploy sets.

**Shipping the fix is a `.14`-class human gate.** `make publish-layer` republishes the
**publicly shared** `ocel-membrane` layer that every Ocel function on every account attaches,
and then the pin is hand-bumped. Authorizing an app deploy is not authorizing a new version of
the shared runtime.

`OCEL_MEMBRANE_LAYER_ARN` overrides the pin, and that is how `.15` was proven: a throwaway layer
published under a **different name**, deleted afterwards. The shared layer and its pin were never
touched. `yo9b` itself is the one that took the human gate and republished the shared layer.

### `ocelhq-wvag.15` — VERIFIED LIVE on account 363236815301. CLOSED

A real invalidation went **raise → `TAG#` → stream → publisher → both stores**. The asset bucket
and R2 both hold the probe tag; R2's document showed a non-zero `deployedAt` (genesis-anchored,
hence prunable — the property PR 4's review added) and an advancing `generatedAt`. DLQ empty, all
four alarms OK, the ESM's `LastProcessingResult` moved from "No records processed" to "OK". The
deployed publisher's `CodeSize` is 473419 bytes, byte-identical to the pinned artifact.

**R2 was checked DIRECTLY rather than inferred, and that mattered.**
`workers/isr-writer/src/index.ts` returns **204 for the `"absent"` outcome as well as for a real
publish**, so a 204 from the writer is *not* proof the records landed — it cannot distinguish
"landed" from "there is no snapshot to land in". Do not treat a raise's status as a receipt.

Two further things were proven by the same run:

- **The Cloudflare DO migration fix works live** (`3b6409d` + `eef4de8`, formerly `779ec2b` +
  `77de9c9`). `ocel-isr-writer-preview` carries **both** `IsrDeploy` and `IsrSnapshot` with
  separate namespace ids, and bootstrap settled clean on an already-bootstrapped account.
- **`PolledEventCount` really does emit `0` for an IDLE-but-healthy mapping** — three consecutive
  5-minute periods of `Sum = 0.0` with no records flowing, alarm at OK. That was the one
  falsifiable prediction `.13`'s design rested on, and it is now measured rather than reasoned
  about.

### `ocelhq-wvag.13` — the staleness alarm. CLOSED, on `isr-herd/06a-publisher-alarms`

**The issue's premise was overturned. Do not re-litigate it.** Age of `generatedAt` cannot report
on the publisher at all:

- The **R2** copy's `generatedAt` is advanced by the snapshot Durable Object's own 60 s heartbeat
  whether or not the publisher feeds it. The heartbeat **masks** a wedged publisher — the exact
  failure the alarm exists to catch.
- The **S3** copy has no heartbeat, so its age just means "nothing has been invalidated lately",
  which is not a fault.

That kills *both* options the issue proposed — a custom metric per publish, and a scheduled probe
emitting snapshot age — because both would alarm on a substrate with no invalidation traffic,
reproducing PR 4's "alarm lit permanently from the moment of bootstrap" finding.

**Replaced by three alarms on metrics Lambda already emits**, opt-in via `MetricsConfig:
Metrics: [EventCount]` on the event source mapping. No probe, no custom metric, no new
infrastructure, no change to the artifact:

- **`PolledEventCount`, `TreatMissingData: BREACHING`** — died outright. Lambda emits `0` on every
  empty poll and emits *nothing at all* from a stopped mapping, so **absence is the signal**.
- **`FailedInvokeEventCount` > 0** — runs but cannot publish. It counts any response with a
  non-empty `BatchItemFailures`, i.e. `publishAll`'s own per-build failure report, and fires five
  retries before anything reaches the DLQ the existing alarm watches.
- **`IteratorAge` > 5 min** — keeping up badly. Thresholded at five of the DO's 60 s heartbeats so
  a beat of jitter cannot flap it.

**`MetricsConfig` is load-bearing, not decorative.** Without it the first two metrics are never
emitted, and the breaching alarm would be lit permanently rather than merely weakened.

### `ocelhq-wvag.6` — **CLOSED**, on `isr-herd/06-get-drops-batchget` (`776d806`)

Its last gate, `ocelhq-yo9b`, is closed; the branch tip is that gate's own membrane pin, one
commit above `.6`'s implementation (`162660a`). `get()` no longer reads DynamoDB: `readTags` and its
BatchGetItem loop are deleted, and tag expiry comes from the shared tag clock through a new
`tagsExpireEntry(tags, lastModified)` in `tag-clock.mts`.

**A design choice the issue did not specify:** it reuses PR 5's existing 2 s throttle and
in-flight join rather than adding a per-request `readTagSnapshot` to `CacheStore`. A snapshot GET
per `get()` would have swapped one per-request network read for another. The clock also gives the
singular tier **read-your-own-writes**, which the DynamoDB read never had — `revalidateTag`
already feeds the same map through `recordTags`.

It **fails open with no fallback**, deliberately the opposite of the remote `use cache` tier,
which stays fail-closed until first sync. Both are commented at the site as load-bearing and
**not to be harmonised**.

`dynamodb:BatchGetItem` is dropped from `isrPolicy`; `UpdateItem` stays, because `writeTags` and
the plural tier's `writeTag` both still need it.

Verified: `lambda-entrypoints` 199, `next-cache` 42, `workers/nextjs` 545, `next-runtime` 157,
`cloud/aws` build and test clean. Eight mutations applied, each caught by the named test.

Two honest weaknesses, recorded rather than papered over:

- **One `null`-document test passes both ways against one specific mutation.**
  `use-cache-store` wraps the call in a try/catch, so a `TypeError` lands on the same `unusable`
  branch. It does catch the production line that matters; it is weaker than it reads.
- **A latent test-isolation bug was found and fixed.** The tag clock lives on `globalThis` and
  survives `vi.resetModules()`, so unbinding it in the wrong order silently broke an unrelated
  plural-write test. That was already true; nothing in that file read the clock before.

### Two small facts worth carrying

- **Next 16.2.10's public `revalidateTag` takes the cache-life profile as a REQUIRED second
  argument** — `revalidateTag(tag, profile)`. The one-argument form is `updateTag`, which is
  callable only from a Server Action. (This is the `next/cache` API, distinct from the cache
  *handler* method names in "Standing constraints".)
- **A new e2e probe exists:** `scripts/e2e-next/assert-tag-publisher.mjs`, plus the smoke-app
  route it drives. Left behind by `.15`.

**`ocelhq-wvag.14` — done, and a live bootstrap attempt exposed a Cloudflare defect. Both on the
PR 7 branch, NOT pushed. `.14` CLOSED; `.15` filed.**

Four commits, taken between PRs like `.12` was (shas rewritten by the restack):

- `fdf045c` (was `cc97e5e`) — pins `tag-publisher-v0.0.1` and its digest. See the `.14` section
  below for what was verified and what was split into `ocelhq-wvag.15`.
- `3b6409d` (was `779ec2b`) — **bootstrap was broken for every already-bootstrapped account** and
  this fixes it. Durable Object migrations are now decided on the deployed classes, not the
  migration tag, because the Cloudflare API does not report a tag anywhere. Full detail in the
  PR 4 section.
- `eef4de8` (was `77de9c9`) — review follow-up: a binding carrying `script_name` names another
  script's class and must not count as deployed here.
- `d9cc24b` — corrects `fdf045c`'s own now-stale placeholder comment. It rewrites the block
  `fdf045c` introduces, so it must stay ordered behind it.

**The order of events matters for whoever picks this up.** `.14`'s acceptance needed a real
bootstrap; the first bootstrap failed on the *Cloudflare* edge before it ever reached AWS. That
is now resolved — see `ocelhq-wvag.15` above: the migration fix and the publisher are both proven
live on `363236815301`.

Verified: `cloud/edge` and `cloud/aws` build and test clean, `gofmt` clean. Five mutations
checked, each caught by the intended test — including reverting to the original bug. The one
`gofmt -l` hit, `cloud/edge/cloudflare/cloudflare_test.go`, is the pre-existing drift from
`b17467f` and was deliberately left alone.

**`ocelhq-wvag.12` — done, on the PR 7 branch, NOT pushed. Issue CLOSED.**

Commit `7c63132` (was `6928e98`), taken between PRs as planned. `cacheKey` and `variant-headers.json` now
have one spelling each, in `packages/next-cache/src/naming.mts`, exported at the subpath
`@ocel/next-cache/naming`. PR 3's contract tests are gone: the property they policed is true
by construction.

**It needed no packaging change, and the issue's stated blocker was wrong on two counts.**
Do not re-derive this — it was established by running the builds, not by reading:

- **The artifact that ships is already an esbuild bundle.** `cli/platform/build-platform.mjs`
  bundles `packages/next-runtime/src/next-adapter.mts` directly, so "next-runtime cannot import
  it as shipped" was only ever true of the *dev* `tsc` dist at `packages/next-runtime/dist/`.
- **Node's type-stripping works through the pnpm symlink.** The blocker was narrower than "raw
  `.mts`": `index.mts` re-exports `./tag-index.mjs`, and Node does not rewrite that specifier.
  A leaf `.mts` that imports nothing loads fine.

**Avoiding a dist build was the point, not the shortcut.** With `"."` resolving to dist,
vitest, esbuild and wrangler would all have started reading compiled output instead of source,
across six build sites, in a repo with no turbo to order them. The `"."` export is untouched
and every existing importer resolves identically.

**The one thing to know before touching `packages/next-runtime`:** adding the workspace
dependency made a new mistake writable. An `import { entryObjectKey } from "@ocel/next-cache"`
in the adapter's source typechecks, passes all 157 next-runtime tests, both wrangler builds and
both esbuild builds — and breaks `next build`, because every other `@ocel/*` consumer is
bundled and resolves the graph itself. That is why the guard is
`packages/next-runtime/test/plain-node-imports.test.mts` and not a test beside the module: it
resolves each specifier the source imports the way the adapter itself resolves it, from
`packages/next-runtime`, over the real `node_modules` link. Mutation-checked against both ways
a module stops being loadable — an added import, and syntax Node cannot erase (an `enum`).

Verified: `next-cache` 42, `next-runtime` 157, `lambda-entrypoints` 197, `workers/nextjs` 545,
`workers/isr-writer` 70, `tag-publisher` 15, `cli-platform` 38; `pnpm -r --no-bail typecheck`
clean except `examples/next-cache-lab` (see "Standing notes"); all five build paths green and the built adapter
dist loads under plain Node.

Two things came out of the review and were **filed rather than fixed**:

- **`ocelhq-heo2`** — `packages/next-runtime/tsconfig.json` includes only `src/**`, so its test
  files are never typechecked. Pre-existing; this change makes it load-bearing.
- **Node < 22.18** (or `--no-experimental-strip-types`) breaks the dev `tsc` dist at
  `next build` with `ERR_UNKNOWN_FILE_EXTENSION`. Loud, not silent, and the new guard fails
  identically, so CI catches it. CI pins `node-version: 22`, floating. The shipped path is
  bundled and unaffected.

**PR 7 (`ocelhq-wvag.7`) — code complete and reviewed, NOT pushed. Issue CLOSED.**

Branch `isr-herd/07-edge-l0-l1`, now rooted on **PR 6** (whose tip is now `776d806`; it was `162660a` when this was written) after the restack; it was
originally branched off PR 5, jumping the then-gated `.6`, and its issue was closed with
`--force` past that dependency edge. The rebase was mechanical, as predicted — `.6` is the origin
Lambda, `.7` is the edge worker. Serves decisions 9 and 12. Two commits (`80deb26`, `e826284`),
both in `workers/nextjs`: nothing else in the repo is touched, and no Go changed.

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
typecheck` clean except `examples/next-cache-lab` (see "Standing notes"). Every new assertion was
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
`workers/isr-writer` 70; `pnpm -r --no-bail typecheck` clean except
`examples/next-cache-lab` (see "Standing notes"); `cloud/aws` builds and tests clean.

Two `pnpm -r test` failures are **pre-existing and unrelated** — confirmed by running both
suites at `c887a83`, where they fail identically: `@repo/api` (9 files) and
`@ocel/provider-aws` (`Cannot find package 'ocel/config'` — the dogfooded SDK is not built).

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
`examples/next-cache-lab` (see "Standing notes"); `cloud/aws` and `cloud/edge` build and test clean.

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
  Collapsing them to one derivation was filed as **`ocelhq-wvag.12`**, on the belief that it
  needed a packaging change. It did not, and the contract tests are gone — see "Current
  position".
- The acceptance criterion "segment prefetch verified" was asserted only as bytes on the
  entry; nothing drove a rewritten entry through `reconstructSegment`, the consumer that
  returns null and disables PPR without `segmentHeaders`. Now covered in `workers/nextjs`,
  both directions, and the negative was mutation-checked to confirm it has teeth.
- Comment/name cleanups: `PrerenderGroup.key` → `entryKey` (the name now carries what four
  lines of comment did), and a paragraph duplicated verbatim across two packages kept once.

Verified after the fixes: `packages/next-runtime` 157; `packages/lambda-entrypoints` 217 of
218 (the one failure is the known pre-existing `test/tag-clock.test.mts`, identical on the
base commit); `workers/nextjs` 525; `packages/next-cache` 34; `pnpm -r --no-bail typecheck`
clean except `examples/next-cache-lab` (see "Standing notes"), which fails on the base commit
identically. No Go changed — a
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
(the `examples/*` failure is pre-existing — see "Standing notes");
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
  per-colo L1 sentinel does what PR 7 assumes. **But suppression does not open at 0**: the
  earliest cross-isolate hit was 201–251 ms after the write. **`.9` superseded that number —
  it is 8 ms, and PR 1's 201 ms never contained the window at all. See `.9`'s section.**
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

The term that actually sizes L2 is the **write-visibility window** between `cache.put` returning
and the sentinel becoming readable from other isolates — measured by `.9` at **8 ms**, not the
~200 ms this paragraph originally assumed. Requests
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
- Correct Next 16.2.10 **cache-handler** method names: `updateTags(tags, durations?)` on the
  plural handler, `revalidateTag(tags, durations?)` on the singular. `expireTags` and
  `receiveExpiredTags` do not exist.
- Distinct from those, the **public `next/cache` API**: `revalidateTag(tag, profile)` takes the
  cache-life profile as a **required** second argument in 16.2.10. The one-argument form is
  `updateTag`, and it is callable only from a Server Action. A probe route that calls
  `revalidateTag(tag)` will not compile.
- Cloudflare Tiered Cache is deliberately not relied on — `originBlocking` sends a
  SigV4-signed request carrying `x-prerender-revalidate` specifically to bypass caching.

## Next step

**The deploy happened, and it superseded the previous plan on this page.** The old "everything
converges on one deploy" framing is spent: bootstrap ran, the migration fix and the publisher are
proven live, and `.13`, `.14` and `.15` are all closed. What the deploy bought was one thing the
plan did not anticipate — it found `ocelhq-yo9b`.

**`ocelhq-yo9b` is the single gate.** It blocks `.6` (implemented and sitting on
`isr-herd/06-get-drops-batchget`) and `.10`. Shipping it means republishing the shared
`ocel-membrane` layer and hand-bumping `defaultMembraneLayerARN` — a human release decision of
exactly the `.14` / `ocelhq-pf6q.13` class. Nothing else in the stack moves until it does, because
until then *no deployed function anywhere can raise a tag*.

Remaining state:

- `ocelhq-wvag.6` — **closed**; `ocelhq-yo9b` cleared and its membrane pin is the branch tip.
- `ocelhq-wvag.8` — **unblocked**. `.7`, `.9` and `.16` are all closed.
- `ocelhq-wvag.9` — **done and closed**, measured live on a zone route across two deploys, both
  torn down.
- `ocelhq-wvag.10` — **unblocked** by `ocelhq-yo9b` closing; its assertion can now be driven.
- `ocelhq-wvag.16` — **done and closed**: measured on a third deploy, built, reviewed twice, fixed.
- `ocelhq-wvag.17` — filed, blocked on `.8`.

(The paragraphs immediately above are the standing record as written at the time and are left
intact; the list here is the current state.)

**Open and ungated, so pickable right now:** `ocelhq-wvag.11` (destroy leaves per-build writer DO
instances behind) and `ocelhq-heo2` (`packages/next-runtime/tsconfig.json` never typechecks its
tests), which came out of `.12`'s review.

Five review rounds have now landed twelve real defects rather than polish, and **in every
single case the failure was silent**: a write dropped with no log, a herd at the auth boundary,
storage written for a caller who never authenticated, a projection that would miss forever if
two derivations drifted, a snapshot that grows unboundedly because it was created without an
anchor, one poison build quietly taking its batch-mates' invalidations to a DLQ, an alarm lit
permanently from the moment of bootstrap, a route that stopped refreshing colo-wide because its
admission record was only ever released on a throw and nothing on that path throws. Not one of
them threw. Keep weighting reviews toward *what fails without saying so* — that is where this
stack's bugs actually live.

**The thirteenth defect broke that pattern, and its lesson is different.** The Cloudflare
migration bug failed *loudly* — a 400 on the first bootstrap. It shipped anyway, because its unit
test stubbed a `migrations` object the real API never returns. The test did not merely fail to
catch the bug; it **asserted the false premise**, and passing it was read as evidence the shape
was right. A fixture invented to match an assumption can only ever confirm it. Where behaviour
depends on an external API's response shape, pin a **captured real response** — the fix's test
carries the verbatim body, so the premise is now falsifiable by re-fetching rather than by
reasoning.

**The fourteenth defect (`ocelhq-yo9b`) is silent again, and its lesson is that no local gate
could have caught it.** Four PRs of Lambda-side work passed every unit test, typecheck, mutation
check and review while *none of it was running on a deployed function* — the deploy renders the
new env surface, the layer inside the function is a hand-bumped pin, and the two drifted apart
with nothing comparing them. The failure then hid behind a 200 because the throw happens after
the response is piped. **Where a component's version is a pinned constant, "the code is correct"
and "the deployed thing runs the code" are separate claims, and only a live run distinguishes
them.** That is the argument for driving a real deploy earlier in a stack rather than at its end.

**The fifteenth and sixteenth defects were in the INSTRUMENT, not the product, and that is the
lesson.** `.16`'s reviews found `zero-claims` reported as an empirical result when it is a
theorem — the globally first `match` on a fresh key must miss, so the detector cannot fire —
and `lowerBound` guarding on dispersion's *median* when the failure it exists for lives in the
tail: at `J = 2000` the median was 1.51 ms and the p90 was 115 ms, fourteen times `W`, and those
rows printed clean over an instrument that had stopped holding the arrivals concurrent. Neither
is a bug in `workers/nextjs`. Both would have put a green number in a document that `.8` sizes
against, which is worse: a product defect gets caught by the next test, and a measurement defect
gets *quoted*.

This is now the fourth and fifth time a detector in this stack was true by construction or blind
to its own failure — `invariantViolations`, `dispersionMs` measuring a JS loop, and the fixed
socket pool came before them. **Where a measurement is the deliverable, the instrument needs the
same adversarial review the product code gets**, and specifically: for every detector, name the
input that makes it fire, and check that the driver can actually produce that input. A detector
that reads zero and cannot be shown to fire is not evidence; label it in the write-up rather
than citing it.

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
  `workers/nextjs/src/cache.ts` rested on the spike's ~200 ms cross-isolate visibility and its
  exact 1–60 s TTLs. `ocelhq-wvag.9` has since measured the write-visibility window at **8 ms**,
  which is what tells you how much L1 leaks into L2 — do not size L2 off the epic's original
  `1 − crossIsolateHitRate` formula, which degenerates to zero. The 5 s constant is unaffected;
  its comment has been corrected under `.16`.
- **L1 now jitters its admission, and L2's sizing depends on it.** `admissionJitterMs = 1000`
  takes the per-colo escape count from ~55-62 to ~1.4 (`ocelhq-wvag.16`). Size L2 against
  `F ≈ 423-438` per stale event — **which is 423-438 rps**, since it lands inside `J = 1 s` —
  not against the 20 000-23 000 the un-jittered path produced. The margin against Cloudflare's
  conservative 500 rps is ~1.2×, and `C ≈ 300` is an assumption; see "`ocelhq-wvag.16`". If the
  jitter is ever removed or moved off the pre-claim path, that sizing is void along with `W`
  itself.
- **The draw is capped by the entry's remaining stale window, and L2 needs the same discipline.**
  Deferring a refresh past the point its own entry stops being servable does not delay the herd,
  it *uncovers* it: past expiration the tier declines to serve and there is no dedupe left below.
  Any wait L2 introduces has to be bounded by the same window.

Live threads to carry forward:

- `ocelhq-wvag.9` is **closed and re-measured**, so `.8` is unblocked and is the stack's last PR.
  Read `.9`'s section for the two numbers it changed and the one it found by accident.
- `ocelhq-yo9b` blocks `.6` and `.10`, and is the only remaining gate on either. `.13`, `.14` and
  `.15` are all closed. See the stack shape above.

## The decisions waiting on a human

`ocelhq-wvag.10` — live e2e for the writer — needs authorization, not just scheduling.
Running a throwaway probe on a zone route (PR 1) and standing up **account-level
infrastructure** are different things, and the second has not been approved.

**Partly overtaken by events.** `.15`'s run stood the account-level infrastructure up:
`ocel-isr-writer-preview` exists on the Cloudflare account carrying both DO classes, and the
publisher Lambda, its DLQ and its ESM exist on `363236815301`. `.10` itself remains open and is
now blocked by `ocelhq-yo9b` — with the fleet's membrane layer unable to raise a tag, the
Lambda → writer → R2 path cannot be driven from a deployed function.

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

**PR 4's migration-tag risk fired, and it was worse than predicted — now fixed.** The
generalization assumed the script-settings endpoint reports a migration tag for a script
migrated with the *older single-tag form*. The first real bootstrap hard-failed on exactly the
already-bootstrapped account it exists to migrate:

```
PUT .../workers/scripts/ocel-deployments-store-preview: 400 code 10074
"Cannot apply new-sqlite-class migration to class 'DeploymentsStore' that is
 already depended on by existing Durable Objects"
```

**No migration tag is reported at all** — not by the settings call, the script list, the version
list or a version's detail. `settings.Migrations` comes back fully zeroed for a script that
demonstrably carries its class. That `""` was indistinguishable from `pendingMigrations`'
documented "never migrated", so the upload redeclared the whole log.

Fixed in `3b6409d` + `eef4de8` (was `779ec2b` + `77de9c9`): the decision keys on the **classes
the deployed script has**
(`class_name` on each `durable_object_namespace` binding, which the same call *does* report),
declaring exactly the classes it lacks. `old_tag` is gone — it names a tag we cannot read back,
so a concurrent bootstrap is rejected by 10074 rather than by the precondition; still rejected,
never misapplied. A binding carrying `script_name` is ignored, since it names another script's
class.

**The old test stubbed a `migrations` object the real API never returns**, which is exactly why
this shipped green. It is deleted; the replacement pins the verbatim live response as a fixture
in `cloud/edge/cloudflare/durableobjectmigration_test.go`. Do not re-derive the API shape from
docs — it was established by calling all four endpoints.

**The fix is now proven live** (`.15`, 2026-08-04): bootstrap settled clean on the
already-bootstrapped account and `ocel-isr-writer-preview` carries both `IsrDeploy` and
`IsrSnapshot` with separate namespace ids.

### `ocelhq-wvag.14` — cut and pin the tag-publisher release artifact — **DONE**

`tag-publisher-v0.0.1` is cut and both constants are pinned (`fdf045c`). Verified rather than
assumed: the asset downloaded from the public release URL hashes to the pinned digest, and
`pnpm --filter @ocel/tag-publisher zip` reproduces it byte for byte (473419 bytes), so the
reproducible-archive claim behind the pin holds. `artifactPin.pinned()` is now true, so
bootstrap renders the publisher instead of skipping it.

**Its live acceptance was split into `ocelhq-wvag.15`, and that is now closed too** — the
publisher is deployed on `363236815301`, running the pinned artifact byte for byte, and has
carried a real invalidation to both stores. `.6`'s gate passed to `ocelhq-yo9b`, which `.15`
uncovered.

### The ISR write-secret seed's new lifecycle

Not a gate, but it should be looked at rather than discovered later. PR 4 made the seed
persistent account state because the account-level publisher must derive any build's secret and
the alternative was a second credential path. The consequence is that the per-deploy rotation
epic decision 6 describes is now per-*build*: redeploying the same buildId does not rotate, and
that one SSM parameter is forgeable write access to every build's ISR entries and tag clocks,
with no rotation automation. Production and preview are provably separated. See PR 4's entry in
"Current position".
