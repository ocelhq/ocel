# Running one Next.js deploy-adapter e2e suite locally

The `test-e2e-deploy` workflow runs the matrix. Locally you run **one suite** —
to reproduce a CI failure, or to stage a live preview to debug against.

Read `scripts/e2e-next/README.md` first for the account setup this depends on.

## Paths

- Adapter repo: `/home/vndaba/Dev/ocelhq`
- Next.js repo: `/home/vndaba/Dev/next.js` — all `pnpm jest` invocations run here
- Sidecar (prebuilt `ocel` and `@ocel/*`): `/home/vndaba/Dev/ocelhq-work/sidecar`
- The `ocel` CLI is **not on PATH**: `node <adapter repo>/packages/ocel/bin/run.js <args>`

If the Next.js repo is not in your context, stop and tell the user to run
`/add-dir /home/vndaba/Dev/next.js`.

## Isolation model

**One project per run id, one preview per app.** `projectSlugForRun()` is
`e2e-<GITHUB_RUN_ID>` (`e2e-local` when unset), and each temp app is a preview
**ref** inside it — the ref *is* the app directory path
(`previewRefForApp` reads `NEXT_TEST_DIR || appDir`). Consequences:

- Both `ocel preview up --ref <dir>` and `ocel preview rm --ref <dir>` resolve
  the project through the `ocel.config.ts` **in their working directory**, so
  both must run from the app's own directory.
- Set `GITHUB_RUN_ID` to a short greppable token so stranded projects are
  attributable. The run id is truncated to 46 chars inside the slug.
- The project owns the preview domain wildcard, which is an **account-wide**
  claim. **Do not run locally while a CI run is live** — they cannot share the
  account.
- **The harness deletes the temp app dir when a suite finishes**
  (`rmSync` in `test/lib/next-modes/base.ts`, guarded only by
  `NEXT_TEST_SKIP_CLEANUP`). There is nothing left to redeploy in place.
- Never delete an app directory before its teardown succeeds — that makes the
  preview unreclaimable from anywhere.

## Preflight

Hard-stop on any of these; a bad preflight makes the result meaningless.

1. **Credentials are not ambient.** `env | grep OCEL_` returns nothing and no
   `AWS_ACCESS_KEY_ID` is set. They live in `<adapter repo>/.env` (needs
   sourcing; defines `CLOUDFLARE_ACCOUNT_ID` and `CLOUDFLARE_API_TOKEN`) plus
   `~/.aws/credentials`. Confirm which AWS profile to use. Subagents inherit
   *your* env, not the user's shell.
2. **Disposable accounts.** `guard-accounts.sh` is a hard gate and exists
   because this provisions real infrastructure. Do not proceed on assumption.
3. **Proxied wildcard DNS.** The `OCEL_E2E_PREVIEW_DOMAIN` value must exist as a
   Cloudflare record on that zone and be **orange-clouded**. Deploys only verify
   it; a missing or grey record fails every deploy.
4. **`ocel bootstrap --preview` has been run** once on the account.
5. **The sidecar carries `ocel`:**
   ```bash
   test -d /home/vndaba/Dev/ocelhq-work/sidecar/node_modules/ocel \
     || echo "STOP: sidecar needs the one-time repack (see README)"
   ```
   `linkSidecar` hard-fails without it, failing the deploy at link time. Repack
   only when no other run is using the sidecar.

## Screen the suite before running it

Three shapes produce no deploy-mode signal at all and cost the same ~15 minutes
as a real run:

1. **`skipDeployment: true`** — a large fraction of app-dir suites set it.
2. **Describe-scope `isNextDeploy` guard** — the suite substitutes a single
   `should skip for deploy` no-op and "passes" while testing nothing. Screen with
   `grep -l isNextDeploy` and read the hit.
3. **`NEXT_ENABLE_ADAPTER` gating**, in both directions — see the note on that
   variable below.

## Run the suite

From `/home/vndaba/Dev/next.js`, with `<SUITE_PATH>` and `<RUN_ID>` replaced:

```bash
cd /home/vndaba/Dev/next.js && \
set -a && . /home/vndaba/Dev/ocelhq/.env && set +a && \
ADAPTER_DIR="/home/vndaba/Dev/ocelhq" \
GITHUB_RUN_ID="<RUN_ID>" \
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
pnpm jest --runInBand <SUITE_PATH>
```

A suite takes 5–25 minutes. If you background it, block on a single completion
signal rather than polling — returning before it finishes has done nothing.

**Do not "simplify" this block:**

- `NEXT_ENABLE_ADAPTER=1` is **load-bearing**. Several suites compute
  `skipDeployment: !isAdapterTest` from it and, unset, silently replace their
  whole body with one no-op case — no deploy, no signal, exit 0 in under a
  second. It is safe here because `NEXT_TEST_DEPLOY_SCRIPT_PATH` short-circuits
  past every other consumer in `next-deploy.ts`, so it cannot trigger a Vercel
  deploy. Two suites use the **inverted** gate
  (`skipDeployment: isAdapterTest && isTurbopackTest`) and are disabled by this
  combination with `IS_TURBOPACK_TEST=1`.
- `OCEL_E2E_DEPLOY_TIMEOUT_MS` is 9 minutes, deliberately under Jest's
  `NEXT_E2E_TEST_TIMEOUT`. The script's own default is 25 minutes, which would
  let Jest time out first and report a deploy hang as a test failure.
- `HEADLESS=true` matches upstream CI, which sets it in `run-tests.js`. Invoking
  `pnpm jest` directly leaves it unset, and `playwright.ts` reads it as a bare
  boolean — so the run executes `itHeaded` cases upstream always skips.
- `OCEL_ACCESS_TOKEN` / `OCEL_API_URL` are inert — `ocel preview up` makes no
  control-plane call. They are kept to match CI.

`deploy.mjs` prints `[ocel-e2e] preview <ref> of project <slug> in <dir>` to
stderr and persists both in the app dir's `.ocel-e2e.json`. Record them.

## Stage a live preview to debug against

Same command with three changes — this leaves the deployment up:

```bash
  … NEXT_TEST_SKIP_CLEANUP=1 …            # keeps the temp app dir
  # omit NEXT_TEST_CLEANUP_SCRIPT_PATH    # keeps the deployment live
  pnpm jest --runInBand <SUITE_PATH> -t "<one failing test name>"
```

- `NEXT_TEST_SKIP_CLEANUP=1` guards **only** the `rmSync` of the temp dir.
- Omitting `NEXT_TEST_CLEANUP_SCRIPT_PATH` is what keeps the deployment alive —
  `next-deploy.ts` runs the cleanup script only when it is non-empty. This is
  why one deploy is enough; no second `ocel preview up --prebuilt` pass.
- `-t` keeps the test phase to one case and gives a fresh reproduction. **At
  least one test must match**, or Jest never runs `beforeAll` and nothing
  deploys.

Take the app URL from `.ocel/deploy-result.json` (`appUrls[0]`). One preview per
suite, shared by everything debugging it.

**Nothing tears this down automatically.** When finished:

```bash
cd <appDir> && node /home/vndaba/Dev/ocelhq/packages/ocel/bin/run.js \
  preview rm --ref <appDir> --yes
```

If teardown fails, leave the directory in place and report the ref and slug —
its Lambdas, worker scripts and DNS label are still live. To take the whole
project instead:

```bash
ADAPTER_DIR=… OCEL_E2E_SIDECAR_DIR=… \
  node scripts/e2e-next/project-teardown.mjs <slug>
```

## Reading the result

Separate **infrastructure** from **adapter**. Infra is not a bug to debug — it
means the run is broken. Likely causes: missing or grey-clouded wildcard record,
`ocel bootstrap --preview` not run, a `guard-accounts.sh` refusal, the deploy
deadline exceeded, or a build failure. A suite that produced no test results at
all is always infra.

**A 502 or a network abort may be an AWS throttle, not a defect:**

- a genuine origin response carries `x-amzn-requestid`; a throttled one does not,
  and its body is a Cloudflare-generated error page — which can also arrive as a
  semantically **wrong 200**;
- a genuine invocation leaves a `START RequestId` line in CloudWatch;
- an explicit throttle shows `x-amzn-errortype: TooManyRequestsException`.

No `x-amzn-*` **and** no CloudWatch invocation ⇒ infra. Find the Lambdas by tag,
filtering on `ocel:project` — **never** `ocel:app`, which is the constant `app`
for every test app:

```bash
aws resourcegroupstaggingapi get-resources \
  --tag-filters Key=ocel:project,Values=<slug> \
  --resource-type-filters lambda:function
```

Check the account's Lambda concurrency quota before blaming the adapter for
bursts of 502s; throttling concentrates in suites that fire server actions,
because POSTs bypass the cache and hit Lambda every time.

```bash
aws service-quotas get-service-quota --service-code lambda \
  --quota-code L-B99A9384 --query 'Quota.Value' --output text
```

**Environment causes that look exactly like adapter bugs:**

- **Cloudflare RUM** on the preview zone injects `/cdn-cgi/rum` beacons into
  request lists that tests assert on. Disable RUM / Web Analytics on the zone.
- **Cloudflare serves a managed `robots.txt`** that never reaches the worker,
  failing the `robots.txt` metadata test independently of any adapter bug.
- A `routing-manifest.json` sitting in the working tree under
  `platform/edge/cloudflare/workers/entry/` is a **scratch artifact for a
  different app**, not the deployed manifest. The real one is in the app's
  `.ocel/output/apps/app/`.
- `serveStaticAsset` writes into the Cloudflare colo Cache API with content-type
  baked in at upload time, so a worker-only fix will not appear to work against
  a warm PoP. Verify against a fresh deploy.

Compare failures against `baseline-manifest.json` before treating one as new: a
test already listed under its suite's `failed` or `flakey` array is known, and
match the full Jest test name exactly.
