# Running one Next.js deploy-adapter e2e suite locally

The `test-e2e-deploy` workflow runs the matrix. Locally you run **one suite** —
to reproduce a CI failure, or to stage a live preview to debug against.

Read `tests/e2e-next.js/README.md` first for the account setup this depends on.

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
**ref** inside it. The ref is *derived from* the app directory but is **not** the
directory path: `previewRef` (`lib.mjs`) hashes `NEXT_TEST_DIR || appDir` into
`<basename hint>-<sha256 prefix>`, e.g. `/tmp/next-1786560435077-458` becomes
`next-178656-b775867b`. Consequences:

- **Pass the recorded ref, never the directory.** `--ref` is taken literally, so
  `--ref <appDir>` names a ref that does not exist. `preview rm` then reports
  success having removed nothing, and the deployment stays live. Read the ref
  from the app's `.ocel-e2e.json`, or from `deploy.mjs`'s stderr line.
- Both `ocel preview up` and `ocel preview rm` resolve the project through the
  `ocel.config.ts` **in their working directory**, so both must run from the
  app's own directory.
- Set `GITHUB_RUN_ID` to a short greppable token so stranded projects are
  attributable. The run id is truncated to 46 chars inside the slug.
- Previews serve on the substrate's preview domain, which the shared entry
  worker holds — no project claims it. Runs are separated by slug, so a local
  run and a CI run no longer collide over the wildcard, though they still share
  the substrate's store, cache bucket and Cloudflare limits.
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
2. **Disposable accounts, confirmed by hand.** `guard-accounts.sh` gates CI, not
   you: it requires `EXPECTED_AWS_ACCOUNT_ID` and
   `EXPECTED_CLOUDFLARE_ACCOUNT_ID`, which are workflow secrets, and aborts on
   the unset variable before comparing anything. Locally, resolve both yourself —
   `aws sts get-caller-identity` and the `CLOUDFLARE_ACCOUNT_ID` in `.env` — and
   match them against the `E2E_EXPECTED_*` secrets. This provisions real
   infrastructure into whichever account the credentials resolve to.
3. **Proxied wildcard DNS.** The substrate's preview wildcard must exist as a
   Cloudflare record on that zone and be **orange-clouded**. Deploys only verify
   it; a missing or grey record fails every deploy. Check with
   `ocel domain ls --preview`; `unimplemented: 404` there is a stale sidecar
   (item 5), not a missing domain. `ocel domain use` writes the wildcard to the
   SSM parameter `/ocel/edge/preview-domain`, which reads it without the CLI.
4. **`ocel bootstrap --preview --features all` and `ocel domain use '<wildcard>' --preview`
   have been run** once on the account. Without the domain, a preview deploy has
   nowhere to serve: no project declares one of its own.
5. **The sidecar carries `ocel`, and carries it fresh:**
   ```bash
   test -d /home/vndaba/Dev/ocelhq-work/sidecar/node_modules/ocel \
     || echo "STOP: sidecar needs the one-time repack (see README)"
   ls -l /home/vndaba/Dev/ocelhq-work/sidecar/node_modules/@ocel/provider-aws-linux-x64/bin/deploy
   git log -1 --format=%ad -- platform/aws/provider cli pkg
   ```
   `linkSidecar` hard-fails on a missing one, failing the deploy at link time. A
   *stale* one passes that check and then runs superseded Go silently, or rejects
   a new RPC as `unimplemented: 404`; the binary older than the last Go-touching
   commit is the tell. Repack only when no other run is using the sidecar, and
   note that packing does not imply building — the Go binaries come from
   `node scripts/build-native.mjs --host --target cli`.

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
OCEL_E2E_SIDECAR_DIR="/home/vndaba/Dev/ocelhq-work/sidecar" \
OCEL_E2E_DEPLOY_TIMEOUT_MS=540000 \
HEADLESS=true \
IS_TURBOPACK_TEST=1 \
NEXT_ENABLE_ADAPTER=1 \
NEXT_TEST_JOB=1 \
NEXT_TEST_MODE=deploy \
NEXT_E2E_TEST_TIMEOUT=600000 \
NEXT_TELEMETRY_DISABLED=1 \
NEXT_TEST_DEPLOY_SCRIPT_PATH=/home/vndaba/Dev/ocelhq/tests/e2e-next.js/deploy.mjs \
NEXT_TEST_DEPLOY_LOGS_SCRIPT_PATH=/home/vndaba/Dev/ocelhq/tests/e2e-next.js/logs.mjs \
NEXT_TEST_CLEANUP_SCRIPT_PATH=/home/vndaba/Dev/ocelhq/tests/e2e-next.js/cleanup.mjs \
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

**Nothing tears this down automatically.** When finished, take the ref and slug
from the app's `.ocel-e2e.json` — **not** the directory path, see the isolation
model above — and run from the app's own directory:

```bash
ref=$(node -p "require('$appDir/.ocel-e2e.json').ref")
cd <appDir> && node /home/vndaba/Dev/ocelhq/packages/ocel/bin/run.js \
  preview rm --ref "$ref" --yes
```

**`preview rm`'s exit code proves nothing.** It has been observed reporting
`✓ … torn down` while the Lambda, the worker route and the live URL all remain.
Always verify, and treat the sweep as part of teardown rather than a fallback:

```bash
aws resourcegroupstaggingapi get-resources \
  --tag-filters Key=ocel:project,Values=<slug> \
  --resource-type-filters lambda:function
```

Filter on `ocel:project` — **never** `ocel:app`, which is the constant `app` for
every test app. If that returns anything, or to reclaim the whole project:

```bash
ADAPTER_DIR=… OCEL_E2E_SIDECAR_DIR=… \
  node tests/e2e-next.js/project-teardown.mjs <slug>
```

`project-teardown.mjs` is the only path that reliably clears everything,
including the project's `/ocel/rootstack-preview/<slug>` SSM parameter, which
`preview rm` leaves behind even when it does delete the compute.

If teardown fails, **leave the app directory in place** and report the ref and
slug — its Lambdas, worker scripts and DNS label are still live, and deleting
the directory first makes the preview unreclaimable from anywhere.

## Reading the result

Separate **infrastructure** from **adapter**. Infra is not a bug to debug — it
means the run is broken. Likely causes: missing or grey-clouded wildcard record,
`ocel bootstrap --preview --features all` not run, a `guard-accounts.sh` refusal, the deploy
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
  It is injected by Cloudflare's HTML post-processor *after* the entry worker
  returns, so no adapter change can strip it, and it only appears for a browser
  User-Agent — a default curl gives a false all-clear. Check a live deployment
  with:
  ```bash
  curl -sS --compressed -A "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 \
    (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36" <deployment url> |
    grep -c cloudflareinsights
  ```
  Anything but `0` and every exact-request-list assertion in the run is void.
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
