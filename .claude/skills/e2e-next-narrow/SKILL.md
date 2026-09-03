---
name: e2e-next-narrow
description: Drive tests/next-compat/baseline-manifest.json down — verify its entries, cluster what is still red, fix and promote. Usage — /e2e-next-narrow [focus and notes]
disable-model-invocation: true
---

# e2e-next-narrow

The work list is `tests/next-compat/baseline-manifest.json` itself. `$ARGUMENTS` is a
focus — which suites or symptoms to weight first, what landed since the last recording,
what to distrust. With no arguments, pick the order yourself. The session is **AFK**: put
every question you have in your first reply, then decide the rest yourself and keep going.

Read `tests/next-compat/run-suite-prompt.md` (isolation model, preflight, screening a
suite, reading a result) and `tests/next-compat/README.md` (what the manifest is, how
promotion works) before wave 1. This file is only the orchestration on top of them.

Sibling skill: `e2e-next-fix` starts from a CI run and asks *what broke*. This one starts
from the committed manifest and asks *what is still broken* — the manifest is a record of
the past, and half the job is finding out which of it is still true.

## Division of labour

**Shell for the deterministic, agents for the judgment.** Suite runs, builds, deploys and
teardowns are known commands with parseable output — run them yourself, detached, and read
the files they write. An agent that shells out and summarises burns a context window to add
nothing and dies with the session. Spend agents on diagnosing an unknown failure and on
writing the fix.

Every suite run is `tests/next-compat/run-one.sh`, one invocation per suite:

```bash
OUT=<scratch>/wave-1
for suite in <suites>; do
  RUN_ID=$RUN_ID OUT_DIR=$OUT NEXT_DIR=… OCEL_E2E_SIDECAR_DIR=… STAGE=1 \
    nohup tests/next-compat/run-one.sh "$suite" >/dev/null 2>&1 &
done
until ! pgrep -f '[r]un-one.sh' >/dev/null; do sleep 60; done
```

The bracket keeps the wait loop from matching itself. Each suite then has
`$OUT/<name>/fragment.json` — its entry in `baseline-manifest.json` shape, unfiltered —
beside `status`, `staged.txt` and `run.log`.

`run-one.sh` never applies the manifest: `NEXT_EXTERNAL_TESTS_FILTERS` is a CI-only
filter. Every local run is unfiltered, so a `"runtimeError": true` entry still executes
and still yields signal.

## Step 0 — Preflight

Once, before wave 1. Work `run-suite-prompt.md`'s preflight list, hard-stopping on each —
a bad preflight makes every wave that follows meaningless. Two things it does not cover:

- The next.js checkout's ref must match the adapter's `next` pin, and installing there
  takes `--frozen-lockfile --config.confirmModulesPurge=false` or it silently keeps the
  previous ref's `node_modules`.
- `RUN_ID`: a short greppable token. Every deploy this session lands in project
  `e2e-$RUN_ID`, which is what makes stranded infrastructure attributable and reclaimable.

## Step 1 — Read the manifest as a work list

Parse it into one row per suite: entry shape (`failed` cases, `flakey` cases, or
`runtimeError`), and your guess at the cluster it belongs to. Then, before spending a
single deploy on it:

- **Screen every suite** against `run-suite-prompt.md`'s three no-signal shapes
  (`skipDeployment: true`, describe-scope `isNextDeploy`, `NEXT_ENABLE_ADAPTER` gating).
  A screened-out suite is not a fix — it is a manifest entry that can never be earned back
  by a green run, and it costs a full 15-minute deploy to learn that twice.
- **Treat the manifest as unverified.** Entries are hand-edited between recordings and
  fixes land without a full run behind them. Every number in it is a claim your wave is
  about to test.
- **Order by suspected shared cause, not by file order.** The manifest is long because a
  few root causes are wide; the win is a cluster, not a suite.

Finish on a table: suite → cases → suspected cluster → screened in/out, with the ordering
you chose and why.

## Step 2 — Waves

Repeat until the work list is empty. Around five suites a wave, chosen because you believe
they share a cause.

1. **Run them staged** (`STAGE=1`), in parallel, as above.
2. **Diff `fragment.json` against the suite's manifest entry.** Three outcomes, and they
   are not the same job:
   - **Empty fragment** — the entry is stale, something already fixed it. Promote and drop
     it. No debugger, no fixer.
   - **Fragment matches the entry** — real outstanding work. Continue to step 3.
   - **Fragment carries cases the entry does not** — a regression, or a suite that was
     recorded from a partial run. Never promote around it and never quietly widen the
     entry; surface it, and decide whether it joins this session's work or gets recorded
     as new baseline with the reason in the commit message.
3. **Debug: one Opus agent per still-failing suite.** Hand it the suite path, `run.log`,
   `fragment.json`, the preview URL from `staged.txt`, and the wave's other suites with
   their symptoms. It reads code, curls the preview, queries AWS; it does not edit and does
   not deploy. It returns root cause, file and line, the fix to make, which other manifest
   entries that same fix should close, and the check that would prove it.
4. **Cluster the diagnoses yourself**, then dispatch **one Sonnet fixer per cluster** —
   never per suite — each in its own worktree (`isolation: "worktree"`) on its own branch
   in the stack. Invoke the `gh-stack` skill for the stack mechanics.
5. **Verify in the shell**: re-run every suite the cluster claims, from the fixer's
   worktree (`ADAPTER_DIR=<worktree>`, **no** `STAGE`, **no** `ONLY`). `fragment.json` is
   the verdict, and only a full-suite run produces one that can be promoted from.
6. **Promote**: delete the now-passing cases from `tests/next-compat/baseline-manifest.json`
   on the fixer's branch, dropping a suite entry once its `failed` empties and dropping a
   `runtimeError` entry outright to re-enable the file. Never re-record a whole baseline —
   that adopts every new failure alongside the fix.
7. **Tear the wave down** before starting the next: every `staged.txt` reconciled.

A wave that ends with no cases promoted is still progress if it retired entries or proved
a cause — say so plainly rather than rolling the same suites into the next wave.

## Step 3 — Close

Run `project-teardown.mjs e2e-$RUN_ID`, prove it with the tag query in
`run-suite-prompt.md`, submit the stack, and report the manifest's arithmetic: entries and
cases at the start, promoted, retired as unwinnable, added as regressions, left untouched.

## Entries that cannot be promoted

Not every manifest line is a bug you can close, and pretending otherwise burns waves.

- **Screened out** — the suite yields no deploy-mode signal at all. Retire the entry and
  say why in the commit message.
- **Upstream or environmental** — `run-suite-prompt.md` names the ones that look exactly
  like adapter bugs (Cloudflare RUM, managed `robots.txt`, warm-PoP asset caching, Lambda
  throttles). An entry with that cause belongs in the manifest permanently, with the
  reason recorded.
- **Flakey** — a `flakey` case passing once proves nothing. Either make it deterministic
  or leave it; never move it to promoted on a single green run.

The commits are the ADRs: a retired entry's justification lives in the commit message that
removes or annotates it, nowhere else.

## Making a fix visible

A fix that never reaches the deployment reads as a fix that failed. Each layer has its own
path:

- **Edge worker or Next adapter** — in the fixer's worktree,
  `pnpm --filter @platform/cf-entry build && node scripts/build-native.mjs --host --target cli`.
  Previews additionally need the shared entry worker reinstalled with
  `ocel domain use '<wildcard>' --preview`, and that worker is a **global singleton**: it
  serves every preview on the bootstrap, so two worktrees cannot hold different edge
  bundles at once. Serialize edge verification, or stack the edge fixes and verify them
  together.
- **Membrane** (`platform/aws/provider/cmd/membrane/**`, `platform/aws/membrane/**`) and
  **bootstrap functions** (`platform/aws/functions/**`) — `make provider`, repack the
  sidecar, then redeploy; for the bootstrap functions, `ocel bootstrap` after the repack.
  There is nothing to publish or release by hand.
- **Sidecar** — repack only for `ocel/config` resolution or the `@ocel/provider-aws*`
  binaries. Nothing else needs it.

## Guardrails

- **Teardown is mandatory.** The only live deployments at any moment are the ones this wave
  staged, each recorded in a `staged.txt`. `preview rm` reporting success proves nothing;
  the tag query does.
- `ONLY=` is for reproduction only. Its fragment is partial and must never reach the
  manifest.
- Infrastructure is not a bug. `run-suite-prompt.md`'s throttle and infra tests decide
  whether a failure deserves an agent at all.
- The manifest is the outstanding-work list, and it is empty when the adapter is green.
  Every commit this session makes it shorter or explains why a line stays.
