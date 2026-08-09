# Next.js deploy-adapter e2e sweep

## Repos and paths (all absolute, all verified to exist)

- Ocel adapter repo: `/home/vndaba/Dev/ocelhq`
- Next.js repo: `/home/vndaba/Dev/next.js` (branch `canary`) — all `pnpm jest`
  invocations run from here
- Sidecar (prebuilt `ocel` and `@ocel/*` packages): `/home/vndaba/Dev/ocelhq-work/sidecar`
- Per-run scratch: `/home/vndaba/Dev/ocelhq/.coverage` — create it if absent;
  `.coverage/` is already in `.gitignore` and these artifacts are not committed
- **Cumulative state (committed):** `/home/vndaba/Dev/ocelhq/scripts/e2e-next/sweep-state.json`
- Known-issue manifest: `/home/vndaba/Dev/next.js/test/deploy-tests-manifest.json`
- The `ocel` CLI is **not on PATH**. It runs as
  `node /home/vndaba/Dev/ocelhq/packages/ocel/bin/run.js <args>`.

If `/home/vndaba/Dev/next.js` is not in your context, stop and tell the user to run
`/add-dir /home/vndaba/Dev/next.js`.

Read `/home/vndaba/Dev/ocelhq/scripts/e2e-next/README.md` before starting. It is
current and describes the isolation model you depend on.

## Isolation model (read this before reasoning about concurrency)

Each temp app the Next.js harness creates mints its **own Ocel project**, slugged
`e2e-<run id>-<8-char hash of the temp dir>`, and deploys into it as a single
persistent preview. The project namespaces the Pulumi stacks, the deployments
store, the asset prefixes, and the Cloudflare worker scripts and routes — so
parallel suites contend over nothing. Consequences you must respect:

- The slug is **derived, not stored**: `projectSlugForApp` hashes
  `NEXT_TEST_DIR || appDir` with `GITHUB_RUN_ID` (falling back to the literal
  `local` when unset). Same dir + same run id ⇒ same slug.
- **Keep the run id SHORT.** `MAX_SLUG_LEN` in `lib.mjs` is 52, derived from the
  63-char DNS label limit — but that is *not* the binding constraint. The slug
  appears **twice** in the Pulumi stack name
  (`<slug>--preview-<slug>--app--<21-char id>`), which Pulumi caps at 100 chars,
  so the real ceiling is **~31**. A run id of `sweep-2026-07-27a` (30-char slug,
  98-char stack name) works with two characters to spare; anything longer fails
  **after** the infra stack is provisioned, stranding a half-created project.
  Tracked as `ocelhq-e7f`.
- `ocel preview rm --name <slug> --yes` resolves the project through the
  `ocel.config.ts` **in its working directory**. You cannot tear a project down
  from an arbitrary cwd. Teardown must run from that app's directory, or from one
  holding a config that declares the same slug.
- The app inside every project is declared under the constant name `app`.
  Isolation is the slug's job, not the app name's.
- Removing an app's only preview pointer takes its whole project with it.
- **The harness deletes the temp app dir when a suite finishes** (`rmSync` in
  `test/lib/next-modes/base.ts`, guarded only by `NEXT_TEST_SKIP_CLEANUP`). Do not
  plan on re-deploying a finished suite's app in place — it is gone. See Step 3.

## Environment hazards (confirmed 2026-07-27 — check these before blaming the adapter)

These cost the previous run real budget. Every one of them produces failures that
look exactly like adapter bugs.

1. **AWS Lambda `ConcurrentExecutions` quota — RESOLVED, but keep the
   signatures.** The account was capped at **10** (not the 1000 default) until
   **2026-07-27**; it is now **1000**, verified via both `service-quotas
   get-service-quota` and `lambda get-account-settings`. `sweep-2026-07-27c`
   recorded **zero** throttle-classified failures. Tracked as `ocelhq-9d3`.
   - While it was 10, bursts of cache-bypassing requests throttled with `429
     TooManyRequestsException` and surfaced to the browser as a **502** — proven
     by firing 20 concurrent action POSTs at one route: exactly 10×200 + 10×429,
     with exactly 10 `START RequestId` lines in CloudWatch. It also had a silent
     mode: a semantically **wrong 200**, a Cloudflare-generated body served with a
     success status, which looks like an adapter bug and is not.
   - **If you ever see those symptoms again, a throttled request is `infra`, not
     `failed`.** The detection procedure in Step 2 stays for exactly this reason —
     read it as "if you see these", not "expect these". Re-read the quota in
     Step 0 every run rather than trusting this paragraph.
   - The damage was never spread evenly: it concentrated in suites that fire
     **server actions**, because POSTs bypass the cache and hit Lambda every
     time. In the 2026-07-27 run, `actions/app-action` took 13 throttle failures
     and `app-middleware` 2, while `metadata`, `navigation` and `rsc-basic` took
     **zero** running concurrently alongside them.
2. **Cloudflare RUM is enabled on the preview zone.** It injects `/cdn-cgi/rum`
   beacons into request lists that tests assert on. Disable RUM / Web Analytics
   on `*.ocel.site`, or the same two `app-action` tests will keep failing.
3. **Cloudflare serves a managed `robots.txt`** on the zone — ~1860 bytes of
   Content Signals Policy that never reaches the worker. It fails the
   `robots.txt` metadata test independently of any adapter bug.
4. **`workers/nextjs/routing-manifest.json` in the working tree is a red
   herring** — a scratch artifact for a different app (`appName: next-cache-lab`),
   not the deployed manifest. The real one is in the app's
   `.ocel/output/apps/app/`. Two debuggers wasted budget on this.
5. **`serveStaticAsset` writes into the Cloudflare colo Cache API with
   content-type baked in at upload time.** A worker-only fix will not appear to
   work against a warm PoP; verify against a fresh deploy.

## Parameters

- `WAVES` = <N>            # number of waves to run
- `SUITES_PER_WAVE` = 5    # see below
- `SWEEP_ID` = <short token, e.g. `sweep-2026-07-27a`; keep the derived slug ≤31 chars>
- `MODE` = `auto`          # `auto` composes waves from sweep-state.json (Step 1);
                           # `explicit` runs a suite list the user supplies

**Sizing a 4-hour fully-unattended run on this machine** (12 cores, ~31 GiB
available, 551 GB free on `/tmp`):

- `WAVES = 7`, `SUITES_PER_WAVE = 5` → 35 suites, **if the `ocelhq-a9x` fix is in
  place** (`NEXT_PRIVATE_TEST_MODE=e2e` set for the build in `deploy.mjs`).
  Without it every page load burns a hard 10-second hydration fallback.
- `WAVES = 5`, `SUITES_PER_WAVE = 4` → 20 suites, if it is not.

Budget ≈ 15 min/wave × 7 = 105 min, leaving ~135 min for preflight, staging,
triage and artifacts.

Why 5 is the right number, not 2 and not 8: with the Lambda quota raised the
deploy phase is IO-wait and parallelizes freely, so the spike that actually binds
is concurrent `next build`. Sweep-a already ran 5 concurrently on this machine
with contention present. 5 is comfortable on 12 cores; **6 is the ceiling worth
risking unattended.**

Stop after `WAVES` waves even if suites remain. There are **690** `*.test.ts`
suites under `test/e2e/app-dir` (measured 2026-07-27; an older note in this file
said 428). You are sampling, not exhausting — `coverage.html` tracks how far in
you are.

## Step 0 — Preflight (do this yourself, before any subagent)

Hard-stop and report to the user if any check fails. Do not work around a failure
here; a bad preflight makes every downstream result meaningless.

**Unattended runs.** Items 1, 2 and 4 say "confirm with the user"; with nobody
watching, that deadlocks. They must instead be **pre-authorized in the invoking
message** — the AWS profile, that both accounts are the disposable ones, and that
`ocel bootstrap --preview` has been run. If any of the three is not stated there,
stop before dispatching and say which. Items 2 and 3 also have automated halves —
`guard-accounts.sh` and the wildcard-DNS check — and those remain **hard,
non-negotiable gates**. An unattended run hard-stops on them; it never proceeds
because there was nobody to ask.

1. **Credentials are not in the ambient environment.** `env | grep OCEL_` returns
   nothing, and there is no `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY` set. They
   live in `/home/vndaba/Dev/ocelhq/.env` (needs `source`ing; it defines
   `CLOUDFLARE_ACCOUNT_ID` and `CLOUDFLARE_API_TOKEN`) plus `~/.aws/credentials`.
   Confirm with the user which AWS profile/keys to use, and confirm every runner
   command sources them. Subagents inherit *your* env, not the user's shell.
2. **Disposable accounts.** Confirm with the user that the resolved AWS and
   Cloudflare accounts are the disposable ones.
   `scripts/e2e-next/guard-accounts.sh` exists because this suite provisions real
   infrastructure hundreds of times. Do not proceed on assumption.
3. **Proxied wildcard DNS record.** The `OCEL_E2E_PREVIEW_DOMAIN` value
   (`*.ocel.site`) must exist as a Cloudflare DNS record on that zone and be
   **proxied — orange cloud**. Deploys now only *verify* this record; they no
   longer create per-hostname DNS. A missing or grey-clouded record fails every
   deploy. Confirm it exists before dispatching anything.
4. **Preview substrate.** `ocel bootstrap --preview` must have been run once on
   the account (it is account-global, not per-project). `ocel preview up` refuses
   without it. Confirm with the user.
5. **Lambda concurrency quota.** Read it and report it before running anything:
   ```bash
   aws service-quotas get-service-quota --service-code lambda \
     --quota-code L-B99A9384 --query 'Quota.Value' --output text
   ```
   It was raised to **1000** on 2026-07-27. Confirm it is still 1000 and note the
   value in the artifacts. If it has somehow dropped back to 10, hold
   `SUITES_PER_WAVE` at 2 and treat any 502 as `infra` (hazard 1).
6. **The sidecar carries `ocel`.** It was packed before `@ocel/sdk` folded into
   the root `ocel` package, so it may hold only `@ocel/*` — and `linkSidecar`
   hard-fails on that, failing every deploy at link time.
   ```bash
   test -d /home/vndaba/Dev/ocelhq-work/sidecar/node_modules/ocel \
     || echo "STOP: sidecar needs the one-time repack"
   ```
   If it is absent, do the one-time repack in
   `scripts/e2e-next/README.md` ("Repacking the sidecar") before dispatching —
   and only when no other run is using the sidecar.
7. **Smoke run.** Run one throwaway suite yourself, serially, end to end, and
   confirm it deploys, tests, and cleans up. Only then start wave 1.
   (`test/e2e/app-dir/mjs-as-extension/mjs-as-extension.test.ts` is a good
   choice — one test, ~3 minutes.)

## Step 1 — Suite selection and the cumulative state file

`sweep-state.json` is the **one source of truth for everything covered so far**.
`.coverage/` is per-run scratch; the state file is what survives. Its shape:

```json
{
  "schema": 1,
  "lastSweepId": "sweep-2026-07-27a",
  "lastRunAt": "<ISO8601>",
  "suites": {
    "test/e2e/app-dir/metadata/metadata.test.ts": {
      "lastRun": "sweep-2026-07-27a",
      "lastOutcome": "completed",
      "counts": { "passed": 39, "failed": 8, "known": 0, "skipped": 0 },
      "openIssues": ["ocelhq-sae", "ocelhq-8xo", "ocelhq-9q8"],
      "failedTests": { "<full jest test name>": "ocelhq-sae" }
    }
  }
}
```

**When `MODE = auto`, compose each wave by this priority, filling up to
`WAVES × SUITES_PER_WAVE`:**

1. **Verify** — suites whose linked bd issues are now **closed**. Highest value:
   confirms a fix landed, and newly-passing tests can be promoted out of
   `baseline-manifest.json`. Check with `bd list --label e2e-next --no-pager`.
2. **New coverage** — suites with no entry in `sweep-state.json` at all.
3. **Rot check** — one previously-all-green suite per wave, rotating (oldest
   `lastRun` first), to catch regressions in ground already covered.
4. **Skip** suites whose linked issues are all still open. You already know they
   fail; re-running one costs ~25 minutes to relearn it. Say in the report which
   suites you skipped and why.

Do **not** re-run everything each sweep. At ~25 minutes per suite it does not
scale, and the baseline manifest exists precisely so partial runs are still a
valid regression signal.

### Pre-filter every candidate before it is dispatched

Three structural traps have each burned a full wave slot. Screen for all three
**before** composing waves — unattended, a wasted slot is unrecoverable, and a
suite that no-ops costs the same 15 minutes as one that tells you something.

1. **`skipDeployment: true`** — 190 of the 508 app-dir suites set it and produce
   no deploy-mode signal at all. Already counted in `suiteCounts` in
   `sweep-state.json`; make it an explicit pre-dispatch check, not a footnote.
2. **Describe-scope `isNextDeploy` guards.** A suite can bail at describe scope
   with `if ((global as any).isNextDeploy) { it('should skip for deploy', …);
   return }`, substituting a single no-op case — so it "passes" while testing
   nothing. `test/e2e/app-dir/rewrites-redirects/rewrites-redirects.test.ts:12-15`
   does exactly this and cost a full slot. Screen candidates with
   `grep -l isNextDeploy` and read the hit before scheduling it.
3. **Suites formerly blocked by `ocelhq-e7w`** (build-time `EEXIST`, was P0). The
   build-blocking fix landed on branch `fix/pages-static-html` (commits
   `a914478`, `304e93b`) and is live-verified: `ocel build` now succeeds with
   zero `EEXIST`/`EISDIR`, and served pages-router documents carry
   `content-type: text/html` instead of `application/octet-stream`. The branch
   is **not yet merged** — once it lands, this whole blocklist goes away.
   `test/e2e/app-dir/app/index.test.ts` has already been run against the fix
   (project slug `e2e-e7wv-261cb405`) and completes: 86 passed / 15 known-cause
   / 2 un-attributed failed / 5 skipped, no e7w signature in any failure. The
   other seven are **unblocked and are high-value verify candidates** for the
   next sweep once the branch is merged — they have not themselves been run
   against the fix, so treat them as unverified, not confirmed green:
   `test/e2e/app-dir/app/experimental-compile.test.ts`,
   `test/e2e/app-dir/app/useReportWebVitals.test.ts`,
   `test/e2e/app-dir/app/standalone.test.ts`,
   `test/e2e/app-dir/app/standalone-gsp.test.ts`,
   `test/e2e/app-dir/hooks/hooks.test.ts`,
   `test/e2e/dynamic-routing/dynamic-routing.test.ts`,
   `test/e2e/index-index/index-index.test.ts`.

Also maintain `/home/vndaba/Dev/ocelhq/.coverage/_ledger.json` for this run,
recording for every suite dispatched: the suite path, the wave, the temp app
directory, and the **project slug** that app minted. Record the slug even for
failed deploys.

The ledger is the only footprint control you have. A killed runner strands a whole
project — Lambdas, worker scripts, deployments store, DNS label — and there is no
global "list all my projects" call; reclaiming one requires knowing its slug and
having a directory whose config declares it. Related open work: `ocelhq-e8i`
(orphan project sweep).

### Footprint guardrail (mandatory, and load-bearing when unattended)

**Each runner owns teardown of its own project**, and the orchestrator owns a
final sweep over everything else. In `sweep-2026-07-27c` two subagents died
mid-stage on transient API 529s and left staged previews **live**; a human
orchestrator was watching and reclaimed them by hand. Unattended, nothing does —
so the run must **end with an unconditional reclaim pass**, whether or not it
believes anything is stranded. For every slug in `_ledger.json`, run + staged
alike:

```bash
aws resourcegroupstaggingapi get-resources \
  --tag-filters Key=ocel:project,Values=<slug> \
  --resource-type-filters lambda:function
```

Anything still found gets torn down. Two constraints make this fiddly, so plan
for them rather than discovering them at the end:

- `preview rm` resolves the project through the `ocel.config.ts` **in its working
  directory**, so teardown must run from that app's directory (`cd <appDir> &&
  ocel preview rm --name <slug> --yes`).
- Therefore **do not delete an app directory until its teardown has succeeded** —
  deleting it first makes the project unreclaimable by any means this run has.

Report every slug the pass could not clear, with its `appDir`.

## Step 2 — Runner subagents (role: `runner`)

Spawn `SUITES_PER_WAVE` runners in parallel, one suite each. Each runner runs
exactly this from `/home/vndaba/Dev/next.js`, with `<SUITE_PATH>` replaced:

```bash
cd /home/vndaba/Dev/next.js && \
set -a && . /home/vndaba/Dev/ocelhq/.env && set +a && \
ADAPTER_DIR="/home/vndaba/Dev/ocelhq" \
GITHUB_RUN_ID="<SWEEP_ID>" \
OCEL_ACCESS_TOKEN=thisdoesntmatter \
OCEL_API_URL=https://ocel.app \
OCEL_E2E_PREVIEW_DOMAIN="*.ocel.site" \
OCEL_E2E_SIDECAR_DIR="/home/vndaba/Dev/ocelhq-work/sidecar" \
OCEL_E2E_DEPLOY_TIMEOUT_MS=540000 \
HEADLESS=true \
IS_TURBOPACK_TEST=1 \
NEXT_ENABLE_ADAPTER=1 \
NEXT_TEST_JOB=1 \
NEXT_TEST_MODE=deploy \
NEXT_E2E_TEST_TIMEOUT=600000 \
NEXT_TELEMETRY_DISABLED=1 \
NEXT_TEST_DEPLOY_SCRIPT_PATH=/home/vndaba/Dev/ocelhq/scripts/e2e-next/deploy.mjs \
NEXT_TEST_DEPLOY_LOGS_SCRIPT_PATH=/home/vndaba/Dev/ocelhq/scripts/e2e-next/logs.mjs \
NEXT_TEST_CLEANUP_SCRIPT_PATH=/home/vndaba/Dev/ocelhq/scripts/e2e-next/cleanup.mjs \
pnpm jest --runInBand <SUITE_PATH> > /home/vndaba/Dev/ocelhq/.coverage/_run-<label>.log 2>&1
echo "EXIT=$?" >> /home/vndaba/Dev/ocelhq/.coverage/_run-<label>.log
```

Notes on this block, so you do not "fix" it:
- `OCEL_E2E_PROJECT_SLUG` is deliberately absent. It was removed in `3f6034a`; no
  script reads it any more.
- `OCEL_ACCESS_TOKEN` / `OCEL_API_URL` are inert for this flow — `ocel preview up`
  makes no control-plane call. They are kept to match CI.
- `GITHUB_RUN_ID` is set to `SWEEP_ID` so every project slug from this sweep
  carries a common, greppable token. Left unset it degrades to the literal
  `local` and orphans become unattributable.
- `NEXT_ENABLE_ADAPTER=1` is **load-bearing, not decorative**: 8 suites
  (`ocelhq-ktl`) compute `skipDeployment: !isAdapterTest` from this exact
  variable and, with it unset, silently replace their entire body with a
  single `should skip next deploy` no-op case — no deploy, no signal, exit 0
  in well under a second. Omitting it burns a wave slot that looks like a
  pass. Setting it is safe here: the custom-deploy-script path
  (`NEXT_TEST_DEPLOY_SCRIPT_PATH`) short-circuits past every other
  `NEXT_ENABLE_ADAPTER` consumer in `next-deploy.ts` (the Vercel CLI
  team/token branch), so it cannot trigger a Vercel deploy or token lookup —
  it only flips the suite-level gate, which is the intent. Two suites use an
  **inverted** gate (`skipDeployment: isAdapterTest && isTurbopackTest`) and
  become disabled by this combination with `IS_TURBOPACK_TEST=1`; see
  `ocelhq-ktl` before scheduling `test/e2e/middleware-rewrites/test/index.test.ts`
  or `test/e2e/i18n-api-support/index.test.ts`.
- `OCEL_E2E_DEPLOY_TIMEOUT_MS` is set to 9 minutes, under Jest's
  `NEXT_E2E_TEST_TIMEOUT`. The script's own default is 25 minutes, which would let
  Jest time out first and report a deploy hang as a test failure.
- `HEADLESS=true` matches upstream CI, which sets it in `run-tests.js`. Invoking
  `pnpm jest` directly leaves it unset, and `test/lib/browsers/playwright.ts` reads
  it as a bare boolean — so every sweep before this one ran **headed**, executing
  `itHeaded` cases that upstream always skips and that have no baseline to compare
  against.

**Runners must not busy-wait.** A suite takes 5–25 minutes. Launch the command in
the background and block on a single completion signal (e.g. an
`until grep -q '^EXIT=' <log>; do sleep 30; done` waiter), rather than polling in a
loop — a runner that returns before its suite finishes has done nothing.

Each runner must, after the suite finishes, **report the project slug** it used
(`deploy.mjs` prints `[ocel-e2e] project <slug> in <dir>` to stderr, and persists
it in the app dir's state file) plus the temp app directory. The orchestrator
writes both into the ledger.

**No retries.** Record the first run's result verbatim.

**Classify each result into exactly one bucket:**

- `passed` — the test case passed.
- `known` — the test case failed AND its exact name appears under this suite's
  `failed` or `flakey` array in `deploy-tests-manifest.json`. Excluded from the
  failure rate; **no debugger is spawned**. Match the full Jest test name string
  exactly. Only 9 suites appear in that manifest — a suite absent from it is
  expected to pass in full.
- `known-cause` — the failure matches the signature of an **open bd issue** in
  `sweep-state.json`'s `issues` registry. Attributed to that issue; **no debugger
  is spawned.** Counted separately from `known` (which means "in the Next.js
  manifest"). See "Match before dispatch" below — this is the single biggest
  saving available to a repeat run.
- `failed` — the test case failed, is not in the manifest, matches no known
  signature, and is **not a throttle** (see below). This is what gets debugged.
- `infra` — either the *suite* never produced test results because deploy or
  staging failed, **or** the failure is an AWS throttle. Likely deploy-stage
  causes: missing/grey-clouded wildcard DNS record, `ocel bootstrap --preview` not
  run, `guard-accounts.sh` refusal, deploy deadline exceeded, a build failure, or a
  slug that exceeded the Pulumi stack-name budget. This is **not** an adapter bug:
  report it, spawn no debuggers, and surface it to the orchestrator immediately.
  Repeated `infra` results mean the run is broken and the sweep should halt.
- `skipped` — Jest skipped it. Record it with `"status": "skipped"`, exclude it
  from `counts`, and say so. Do not record a skipped test as passed.

**Match before dispatch (mandatory).** Before grouping failures into clusters and
spawning any debugger, run every `failed` candidate against the `issues` registry
in `sweep-state.json`. Each open issue carries `matchers`, each with a
`confidence`:

- `specific` — an exact error string, header, or route shape. **Auto-attribute**:
  bucket the test as `known-cause`, record the issue id, spawn nothing.
- `broad` — a symptom that several causes share (a bare `502`, an
  `ERR_HTTP_RESPONSE_CODE_FAILURE`). **Run the matcher's `check` field first.**
  Only attribute if the check passes; otherwise treat it as `failed`.

Then spawn **one short `confirm` debugger (~5 minute budget) per matched
signature per run** — not per test — to spot-check that the attribution is real.
That is cheap insurance against a genuinely new bug hiding behind a familiar
error, and it is the only debugger a matched cluster gets.

Why this exists: root causes cross suite boundaries. In the 2026-07-27 run
`ocelhq-e7w` produced failures in three separate suites and `ocelhq-7og` in two;
they were only unified because the orchestrator relayed findings between live
agents. A later run has no such memory — without matching, a fresh debugger
spends its full 20-minute budget rediscovering `Download is starting` the first
time it appears in a suite we have not run before. Every newly-covered suite is
an opportunity for this, so the saving grows as coverage grows.

When a debugger files a **new** issue, it must add matchers for it to the
registry, or the next run will rediscover that one too.

**Throttle detection.** The quota that caused this is now 1000 and
`sweep-2026-07-27c` saw zero throttles, so do not go looking. But if a failure's
symptom *is* a 502 or a network-level abort, check it before calling it `failed`:

- a genuine origin response carries **`x-amzn-requestid`**; a throttled one does
  not, and its body is a Cloudflare-generated error page;
- a genuine invocation leaves a **`START RequestId`** line in CloudWatch;
- an explicit throttle shows `x-amzn-errortype: TooManyRequestsException`.

No `x-amzn-*` **and** no CloudWatch invocation ⇒ **`infra`, not `failed`.** In the
2026-07-27 run 15 failures were investigated as adapter bugs before this was
discovered. Find the Lambdas by tag, filtering on `ocel:project` (**never**
`ocel:app` — it is the constant `app` for every test app and matches every
concurrently-deployed app):

```bash
aws resourcegroupstaggingapi get-resources \
  --tag-filters Key=ocel:project,Values=<projectSlug> \
  --resource-type-filters lambda:function
```

**Write results to `/home/vndaba/Dev/ocelhq/.coverage/<slugified-suite-path>.json`**
(suite path with `/` → `-`, `.test.ts` stripped), using **exactly** this schema:

```json
{
  "suite": "test/e2e/app-dir/app-static/app-static.test.ts",
  "wave": 1,
  "projectSlug": "e2e-<sweep id>-<hash>",
  "appDir": "/tmp/next-test-<ts>-<n>",
  "startedAt": "<ISO8601>",
  "durationMs": 0,
  "outcome": "completed | infra",
  "infraError": null,
  "counts": { "passed": 0, "failed": 0, "known": 0 },
  "tests": [
    {
      "name": "<full jest test name>",
      "status": "passed | failed | known | skipped",
      "manifestBucket": null,
      "error": {
        "message": "<jest failure message>",
        "assertion": "<expected/received diff, verbatim>",
        "stack": "<stack trace, verbatim>"
      }
    }
  ]
}
```

`error` is `null` for passing tests. Copy Jest output verbatim — do not summarize,
truncate, or paraphrase it. (Jest embeds ANSI colour codes; keep them in the JSON
and strip them at render time.)

## Step 3 — Staging previews for the debuggers (you, the orchestrator, per wave)

The harness **deletes each temp app dir when its suite finishes**, so there is
nothing left to re-deploy in place. To give debuggers a live preview, re-stage the
suite with a single test selected:

```bash
cd /home/vndaba/Dev/next.js && \
set -a && . /home/vndaba/Dev/ocelhq/.env && set +a && \
ADAPTER_DIR="/home/vndaba/Dev/ocelhq" \
GITHUB_RUN_ID="stg" \
OCEL_E2E_PREVIEW_DOMAIN="*.ocel.site" \
OCEL_E2E_SIDECAR_DIR="/home/vndaba/Dev/ocelhq-work/sidecar" \
OCEL_E2E_DEPLOY_TIMEOUT_MS=540000 \
HEADLESS=true \
IS_TURBOPACK_TEST=1 NEXT_ENABLE_ADAPTER=1 NEXT_TEST_JOB=1 NEXT_TEST_MODE=deploy \
NEXT_TEST_SKIP_CLEANUP=1 \
NEXT_E2E_TEST_TIMEOUT=600000 NEXT_TELEMETRY_DISABLED=1 \
NEXT_TEST_DEPLOY_SCRIPT_PATH=/home/vndaba/Dev/ocelhq/scripts/e2e-next/deploy.mjs \
NEXT_TEST_DEPLOY_LOGS_SCRIPT_PATH=/home/vndaba/Dev/ocelhq/scripts/e2e-next/logs.mjs \
pnpm jest --runInBand <SUITE_PATH> -t "<one failing test name>"
```

Why each piece, so you do not "simplify" it:
- `NEXT_ENABLE_ADAPTER=1` is load-bearing for the same reason as in Step 2:
  omitting it makes `skipDeployment: !isAdapterTest` suites short-circuit to a
  no-op case, so the staging deploy never happens and no debugger gets a
  preview. See the Step 2 note and `ocelhq-ktl`.
- `NEXT_TEST_SKIP_CLEANUP=1` guards **only** the `rmSync` of the temp dir, so the
  staged app survives.
- `NEXT_TEST_CLEANUP_SCRIPT_PATH` is **deliberately omitted**. `next-deploy.ts`
  only runs the cleanup script when that variable is non-empty, so omitting it
  leaves the **deployment live**. This is what makes one deploy enough — there is
  no need for a second `ocel build && ocel preview up --prebuilt` pass.
- `-t "<one failing test name>"` keeps the test phase to a single case, which also
  gives the debugger a fresh reproduction. At least one test must match, or Jest
  never runs `beforeAll` and nothing deploys.
- `GITHUB_RUN_ID="stg"` is short on purpose (see the slug budget above) and marks
  staged projects as distinct from run projects.

One preview per **suite**, shared by all of that suite's debuggers. Capture the
app URL from `.ocel/deploy-result.json` (`appUrls[0]`). If the deploy fails, mark
that suite's failures `infra-blocked` and skip its debuggers.

**Nothing tears these down automatically.** Record every staged slug and app dir
in the ledger, and reclaim each one when its debuggers finish:

```bash
cd <appDir> && ocel preview rm --name <projectSlug> --yes
```

This must run from a directory whose `ocel.config.ts` declares that slug —
`preview rm` addresses the project through the config in its cwd. Do not delete
the app directory until after teardown succeeds. If teardown fails, leave the
directory in place, record the slug in the ledger as `stranded`, and report it;
that project's Lambdas, worker scripts and DNS label are still live.

## Step 4 — Debugger subagents (role: `debugger`)

**One debugger per root-cause cluster, not per failing test.** Group the failures
first: a suite's failures usually collapse into far fewer defects, and clusters
routinely span suites. In the 2026-07-27 run, 43 failures reduced to 12 defects,
and one issue (`ocelhq-7og`) turned out to own 6 failures across two suites. One
agent per test would have had ~41 agents rediscovering the same bugs and racing
each other's dedupe checks.

Build the clusters from the runners' grouped reports, then spawn one debugger per
cluster with: the suite path(s), every member test's full name and verbatim Jest
error, the shared preview URL(s), and the project slug(s).

**Hard cap: ~10 debuggers per run.** At ~35 suites the binding constraint stops
being wall clock and becomes *your* context — an orchestrator that cannot hold the
run in its head cannot merge clusters, and merging clusters is the whole point of
this step. One debugger per root-cause cluster, ten clusters maximum; if there are
more, take the ten with the largest blast radius and record the remainder as
un-triaged with their verbatim errors. For the same reason, runners must report
**terse grouped clusters, not prose** — a cluster label, the member test names, and
the verbatim error, with no narration around them.

Tell each debugger which neighbouring clusters exist and ask it to **rule
explicitly on whether its cluster collapses into one of them**. That is where the
value is — in the last run it merged three clusters correctly and split one that
looked unified.

**Relay findings between debuggers while they run.** A root cause one agent
confirms often reframes another's cluster; the orchestrator is the only one who
can see across them.

**Triage only — no fixes.** Do not edit any file in either repo. The deliverable is
a diagnosis, not a patch. (Fixing is the job of the sibling prompt,
`fix-prompt.md`.)

**Budget: stop after ~20 minutes of investigation.** If no concrete lead by then,
file the issue as unknown and state what you ruled out.

Each debugger files a bd issue and **returns the issue ID to the orchestrator**:

```bash
bd create "<title>" --type bug --labels e2e-next,needs-triage \
  --description "..." --notes "..."
```

- Title: `e2e-next: <short failure description>`
- Description: the verbatim Jest errors, the preview URL, the project slug, the
  observed HTTP responses, and the diagnosis
- Out of budget with no lead: add label `unknown`, list eliminated hypotheses in
  the notes
- **Before filing, run `bd list --label e2e-next --no-pager`** and check for an
  existing issue covering the same failure. If one exists, comment on it instead
  of duplicating, and return that ID. Note the flag asymmetry: `bd list` takes
  `--label` (singular), `bd create` takes `--labels` (plural).
- Cross-reference coupled issues in **both** directions.

Final message must be exactly the bd issue ID (e.g. `ocelhq-cxh`) plus a one-line
summary — the orchestrator needs both for the artifacts.

## Step 5 — Artifacts

After the last wave, write four files. Two are cumulative and committed; two are
per-run scratch in `.coverage/`.

**1. `scripts/e2e-next/sweep-state.json` (cumulative, committed).** Merge this
run's results into the existing file — do not overwrite it. Add new suites, update
re-run suites, and leave untouched suites alone. This is the answer to "what have
we covered so far"; every consecutive run must build on it. Update all four parts:
`suites`, `runs` (append this run), `issues` (add matchers for every newly-filed
issue; mark fixed ones `"status": "closed"`), and `lastSweepId` / `lastRunAt`.

The entry you append to `runs` **must use exactly these flat keys** — do not nest the
counts under `counts` or `totals`, and do not rename `suites` to `suitesRun`:

```json
{
  "sweepId": "sweep-2026-07-27f",
  "ranAt": "<ISO8601>",
  "waves": 7,
  "suitesPerWave": 5,
  "suites": 35,
  "passed": 98,
  "failed": 12,
  "known": 0,
  "skipped": 1,
  "infra": 0,
  "lambdaConcurrencyQuota": 1000,
  "notes": "..."
}
```

This is load-bearing: `render-coverage.mjs` reads these keys to build the run-history
table. Three sweeps each invented their own shape (`counts.*`, `totals.*`,
`suitesRun`), and because the renderer divided unguarded, every one of them rendered
as `undefined` and `NaN%` rather than failing loudly. The entries have since been
normalised and the renderer now falls back across the legacy shapes and prints `—`
for a genuinely missing field — but **match the schema above** rather than relying on
that fallback. Do not store a precomputed `passRate`; the renderer derives it, and a
stored copy is a second source of truth that can silently disagree.

**2. `scripts/e2e-next/coverage.html` (cumulative view).** Regenerate it — never
hand-edit it — with:

```bash
node scripts/e2e-next/render-coverage.mjs
```

It is a pure function of `sweep-state.json`, so it is always safe to re-run. It
shows coverage against the 690 suites in the tree, aggregate pass/fail across
everything covered so far, recurring causes ranked by how many tests each owns
(with the suites each has been seen in), the run history, and the known-cause
registry. Keeping it in sync is one command; do not skip it.

Every bd issue id in it deep-links to the local beads kanban UI at
`http://localhost:7070/#/issues?issue=<id>`. Override the host with
`BEADS_UI_URL=<origin>`, or set it empty to render plain unlinked ids. Only
ids matching `^ocelhq-[a-z0-9]+$` are linked — the registry also holds
environment pseudo-issues (`env-cloudflare-rum`, `env-cloudflare-robots`) that
beads has never heard of, and linking those produces confidently broken links.

**3. `/home/vndaba/Dev/ocelhq/.coverage/report.html`** (this run only) — self-contained (inline
CSS, no external assets), openable directly in a browser, and readable in both
light and dark themes:
- Headline: suites run, test cases passed / failed / known / infra, pass rate
  (`passed / (passed + failed)`; `known`, `infra` and `skipped` excluded from both
  numerator and denominator, stated as such on the page)
- **Both rates when throttling occurred**: the raw rate, and the rate excluding
  `infra`-classified throttles, with the quota named. In the 2026-07-27 run this
  was 81.4% raw vs 87.0% adjusted — the gap is the whole reason the distinction
  matters, and a reader who sees only the raw number will draw the wrong
  conclusion about the adapter.
- Per-wave and per-suite breakdown
- **Root causes ranked by blast radius** (bd issue → test count → cause), before
  the per-test detail — that is the actionable view
- For each `failed` test: suite, test name, verbatim Jest error in a `<pre>`, bd
  issue ID and diagnosis
- Every bd issue id rendered anywhere on the page must deep-link to the beads
  kanban UI, same rule as `coverage.html` above: `<a href="${BEADS_UI_URL}/#/issues?issue=<id>">`,
  default origin `http://localhost:7070`, and link only ids matching
  `^ocelhq-[a-z0-9]+$` so cluster placeholders like `(un-triaged)` and the
  `env-*` pseudo-issues stay plain text
- Collapsed sections for `known` failures and `infra` failures
- A **manifest drift** section: manifest entries that now pass (delete them from
  `baseline-manifest.json` per the README's promotion rule) and `known` failures
  that share a root cause with an open issue
- An **environment findings** section for anything in "Environment hazards" that
  this run hit
- A **stranded projects** section listing any slug whose teardown failed, with the
  exact `cd <appDir> && ocel preview rm --name <slug> --yes` command to reclaim it
- **Cumulative coverage** from `sweep-state.json`: how many of the 428 suites have
  ever been run, and when

**4. `/home/vndaba/Dev/ocelhq/.coverage/failures.json`** (this run only):

```json
{
  "generatedAt": "<ISO8601>",
  "sweepId": "<SWEEP_ID>",
  "wavesRun": 0,
  "totals": { "suites": 0, "passed": 0, "failed": 0, "known": 0, "infra": 0 },
  "strandedProjects": ["e2e-<sweep id>-<hash>"],
  "failures": [
    {
      "suite": "test/e2e/app-dir/.../x.test.ts",
      "test": "<full jest test name>",
      "summary": "<one line>",
      "bdIssue": "ocelhq-xxx"
    }
  ]
}
```

bd issues are local to the beads DB — `bdIssue` is the ID string, not a URL.

Verify before you finish: every `failed` test maps to a `bdIssue` (or an
explicitly-recorded environment artifact), and the mapped count equals
`totals.failed`. A silent gap here means a real failure went un-triaged.
