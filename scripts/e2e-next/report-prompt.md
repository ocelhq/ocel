# Next.js deploy-adapter e2e coverage report

Sibling of `sweep-prompt.md` (finds bugs locally) and `fix-prompt.md` (fixes
them). This one **reads a completed `test-e2e-deploy` run on GitHub** and turns it
into a coverage report: overall pass rate, suites ranked by failures, failures
clustered by root cause, and a fix order that avoids debugging the same defect
twice.

You are given a **run id** and nothing else. Everything below is derived from
GitHub. Do not use local sweep artifacts, `.coverage/`, or a previously committed
`baseline-manifest.json` as your source of truth — they describe a different run.

## The one rule that matters

**The job logs are the authoritative record, not the baseline artifacts.**

The `baseline-*` artifacts are a *product* of the run and can under-record it. The
logs contain, for every suite, the complete Jest JSON the harness printed. Derive
your numbers from the logs and use the artifacts only to cross-check.

If your log-derived suite count and the merged manifest's suite count disagree,
that disagreement **is a finding** — chase it before writing anything else.

## Step 1 — Inventory the run

```bash
gh run view <run-id>
gh api repos/{owner}/{repo}/actions/runs/<run-id>/jobs --paginate \
  --jq '.jobs[] | "\(.id)\t\(.name)\t\(.conclusion)"'
gh api repos/{owner}/{repo}/actions/runs/<run-id>/artifacts \
  --jq '.artifacts[] | "\(.id)\t\(.name)\t\(.size_in_bytes)"'
```

Note the matrix width from the job names (`Tests (N/M)`) — do not assume it. Map
API job id → group number from those names; you need it to attribute suites to
groups later. **Artifact sizes are a smell test**: fragments should be roughly
even, and a conspicuously small one means that group recorded almost nothing.

## Step 2 — Pull the logs and the artifacts

```bash
gh run download <run-id> -D <scratch>/artifacts        # run from inside the repo
for j in <job-ids>; do
  gh api --allow-escape-sequences "repos/{owner}/{repo}/actions/jobs/$j/logs" \
    > "<scratch>/logs/$j.log" &
done; wait
```

`--allow-escape-sequences` is required — the logs carry ANSI and `gh` refuses to
emit them without it. Expect tens of MB. Fetch the jobs in parallel.

The GitHub UI's per-job summary (`.../jobs/<internal-id>/summary_raw`) needs a
browser session, not a PAT, so it is not reachable from here. You do not need it:
it is rendered from the same Jest output the logs already contain.

## Step 3 — Extract the ground truth

Every log line is prefixed with an ISO timestamp added by the log API, and may
contain ANSI. Strip both before matching anything.

The harness frames each suite's full Jest JSON on a single line:

```
--test output start-- {...} --test output end--
```

Parse those into `{suite, results}`. `testResults[0].name` is an absolute path on
the runner; the suite key is the part from `test/` onward. This should yield
**one entry per suite actually executed** — verify that count against
`groups × suites-per-group` before trusting anything downstream.

From the same logs also collect:

- `Starting <suite> retry <n>/<m>` — every suite the harness began.
- `<suite> finished on retry <n>/<m>` — suites that completed.
- `Cleaning test files at <dir>` — the harness's between-retry `git clean -fdx`.
- `[ocel-e2e] collected N suite results into <fragment>` — what each group
  actually recorded.

Then compute the toplines from the parsed JSON, never from the manifest:
suites executed, cases passed / failed, failing suites, and suites where every
case is `pending`.

## Step 4 — Reconcile against the artifacts

Diff your log-derived suite set against the merged `baseline-manifest` artifact
and each group fragment. For any suite present in one and not the other, or
disagreeing on pass/fail, find out why. Known traps:

- **`runtimeError` does not mean "crashed."** `lib.mjs` sets it when a suite
  yields zero passes *and* zero failures. A suite whose cases are all `pending`
  under deploy mode (guarded fixtures — PPR, yarn-pnp, dev-only suites) lands
  there too. Classify each one from its JSON (`numPendingTests` vs
  `numTotalTests`) before reporting any of them as broken. Reporting skipped
  suites as crashes is the single easiest way to send the reader chasing ghosts.
- **Secret redaction corrupts log-derived strings.** GitHub replaces any repo
  secret or variable value with `***` wherever it appears, including inside test
  names and file paths. If a suite key or case name contains `***`, prefer the
  artifact's copy (written on the runner, pre-redaction) and flag the redaction —
  a secret whose value is a common word is its own bug.

## Step 5 — Cluster the failures by cause, not by message

A per-suite failure count is not a plan. Group failures by what one fix would
close. Work from each failure's own message *and* its surrounding log context:

- **The assertion message is often not the cause.** A deploy-script failure
  surfaces as an opaque `Custom deploy script failed: undefined (1)`; the real
  error (an unsupported feature, a build invariant, a missing env var) is only in
  the harness stdout around it. Segment the log per suite and read back from the
  failure to the first real error line.
- **Cluster on the error, then look for shape.** Suites failing on the same
  selector, the same status code, or the same build error are usually one defect.
  A family of sibling suites each failing the *same small number* of cases is a
  strong single-defect signal.
- **Separate infrastructure from product.** Cloudflare 5xx and runner timeouts
  should be re-run, not debugged. Say so explicitly rather than ranking them.
- **Do not force a cluster.** A long tail of genuinely unrelated per-suite bugs
  is a real finding; label it as such and rank it by failure count.

Report each cluster with: failures, suites, how many of those suites have **zero
passing cases**, and the evidence you clustered on.

## Step 6 — Derive the fix order

The ordering constraint is **information, not priority**.

A suite with zero passing cases died before its tests ran — it is hiding an
unknown number of real failures. Fixing those first makes the visible failure
count *go up*, and that is the point: you want the true surface before anyone
grinds through behaviour bugs.

So the order is:

1. Anything that makes the recording itself untrustworthy.
2. Build/deploy blockers, largest blast radius first.
3. Behaviour clusters — and where several clusters share a layer, sequence them
   so the shared layer is fixed once.
4. Cheap mechanical sweeps, then the long tail by descending failure count.

State the dependencies explicitly ("cluster B must follow cluster A, they share
the middleware path"), and say to re-record between waves rather than at the end.

## Step 7 — Publish

Publish as an artifact. Include: the toplines, any recording-integrity finding up
top (it changes how every other number should be read), the clusters ranked by
blast radius, the wave plan, and a full ranked table of every failing suite with
its group and a blocked/partial marker.

Write what the evidence supports. If a root cause is unproven, say what you know
and what the next check would be — a confident wrong cause costs more than an
honest gap.

## Verification before you publish

- [ ] Suites parsed from logs == groups × suites-per-group.
- [ ] Cases passed + failed reconciles with the per-suite JSON totals.
- [ ] Every `runtimeError` suite classified as skipped-vs-crashed from its JSON.
- [ ] Log-derived and artifact-derived counts either agree, or the difference is
      explained in the report.
- [ ] Cluster sizes sum to the total failure count.
- [ ] Every claim about a root cause names the log line it rests on.
