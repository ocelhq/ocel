---
name: e2e-next-fix
description: Debug, fix and verify Next.js deploy-adapter e2e failures from a test-e2e-deploy run. Usage — /e2e-next-fix <github run id> [notes]
disable-model-invocation: true
---

# e2e-next-fix

`$1` is a `test-e2e-deploy` run id on GitHub. Everything after it is notes — what to
weight, what landed since that run, what to distrust. The session is **AFK**: put every
question you have in your first reply, then decide the rest yourself and keep going.

Read `scripts/e2e-next/run-suite-prompt.md` (isolation model, preflight, reading a
result) and `scripts/e2e-next/README.md` (baseline promotion) before wave 1. This file is
only the orchestration on top of them.

## Division of labour

**Shell for the deterministic, agents for the judgment.** Suite runs, builds, deploys and
teardowns are known commands with parseable output — run them yourself, detached, and read
the files they write. An agent that shells out and summarises burns a context window to add
nothing and dies with the session. Spend agents on diagnosing an unknown failure and on
writing the fix.

Every suite run is `scripts/e2e-next/run-one.sh`, one invocation per suite:

```bash
OUT=<scratch>/wave-1
for suite in <suites>; do
  RUN_ID=$RUN_ID OUT_DIR=$OUT NEXT_DIR=… OCEL_E2E_SIDECAR_DIR=… STAGE=1 \
    nohup scripts/e2e-next/run-one.sh "$suite" >/dev/null 2>&1 &
done
until ! pgrep -f '[r]un-one.sh' >/dev/null; do sleep 60; done
```

The bracket keeps the wait loop from matching itself. Each suite then has
`$OUT/<name>/fragment.json` — its entry in `baseline-manifest.json` shape, unfiltered —
beside `status`, `staged.txt` and `run.log`.

## Step 0 — Preflight

Once, before wave 1. Work `run-suite-prompt.md`'s preflight list, hard-stopping on each —
a bad preflight makes every wave that follows meaningless. Two things it does not cover:

- The next.js checkout's ref must match the adapter's `next` pin, and installing there
  takes `--frozen-lockfile --config.confirmModulesPurge=false` or it silently keeps the
  previous ref's `node_modules`.
- `RUN_ID`: a short greppable token. Every deploy this session lands in project
  `e2e-$RUN_ID`, which is what makes stranded infrastructure attributable and reclaimable.

## Step 1 — Work list

Derive the failing suites from the **run's job logs**, not from the committed manifest —
`report-prompt.md` steps 1–5. The manifest carries hand-edited graduations no run has
verified yet, and the notes may name more. Finish on a table: suite → failing cases →
suspected cluster, with every disagreement between logs and manifest flagged rather than
quietly resolved.

## Step 2 — Waves

Repeat until the work list is empty. Around five suites a wave, chosen because you believe
they share a cause.

1. **Run them staged** (`STAGE=1`), in parallel, as above.
2. **A green suite is done.** An earlier wave's fix reached it — promote its manifest
   entry, tear its preview down, drop it. No debugger.
3. **Debug: one Opus agent per still-failing suite.** Hand it the suite path, `run.log`,
   `fragment.json`, the preview URL from `staged.txt`, and the wave's other suites with
   their symptoms. It reads code, curls the preview, queries AWS; it does not edit and does
   not deploy. It returns root cause, file and line, the fix to make, which other failing
   suites that same fix should close, and the check that would prove it.
4. **Cluster the diagnoses yourself**, then dispatch **one Sonnet fixer per cluster** —
   never per suite — each in its own worktree (`isolation: "worktree"`) on its own branch
   in the stack. Invoke the `gh-stack` skill for the stack mechanics.
5. **Verify in the shell**: re-run every suite the cluster claims, from the fixer's
   worktree (`ADAPTER_DIR=<worktree>`, no `STAGE`). `fragment.json` is the verdict.
6. **Promote**: delete the now-passing cases from `scripts/e2e-next/baseline-manifest.json`
   on the fixer's branch, dropping a suite entry once its `failed` empties. Never re-record
   a whole baseline — that adopts every new failure alongside the fix.
7. **Tear the wave down** before starting the next: every `staged.txt` reconciled.

## Step 3 — Close

Run `project-teardown.mjs e2e-$RUN_ID`, prove it with the tag query in
`run-suite-prompt.md`, submit the stack, and report per cluster: root cause, PR, cases
promoted, cases still red.

## Making a fix visible

A fix that never reaches the deployment reads as a fix that failed. Each layer has its own
path:

- **Edge worker or Next adapter** — in the fixer's worktree,
  `pnpm --filter @platform/cf-entry build && node scripts/build-native.mjs --host --target cli`.
  Previews additionally need the shared entry worker reinstalled with
  `ocel domain use '<wildcard>' --preview`, and that worker is a **global singleton**: it
  serves every preview on the substrate, so two worktrees cannot hold different edge
  bundles at once. Serialize edge verification, or stack the edge fixes and verify them
  together.
- **Membrane** (`platform/aws/provider/cmd/lambdanode/**`,
  `platform/aws/functions/entrypoints/**`) — `make publish-layer` prints the new ARN. Trial
  it through `OCEL_MEMBRANE_LAYER_ARN`; land it by bumping `defaultMembraneLayerARN` in
  `platform/aws/provider/deploy/function.go`.
- **GitHub-released artifacts** (image optimizer, tag publisher, revalidator) — release the
  asset under the tag `<name>-v<version>`, then bump the version and sha256 in
  `platform/aws/provider/bootstrap/<name>version.go`. A stale sha refuses the deploy.
- **Sidecar** — repack only for `ocel/config` resolution or the `@ocel/provider-aws*`
  binaries. Nothing else needs it.

## Guardrails

- **Teardown is mandatory.** The only live deployments at any moment are the ones this wave
  staged, each recorded in a `staged.txt`. `preview rm` reporting success proves nothing;
  the tag query does.
- Judge every failure against `baseline-manifest.json` first — exact full Jest name.
- Infrastructure is not a bug. `run-suite-prompt.md`'s throttle and infra tests decide
  whether a failure deserves an agent at all.
