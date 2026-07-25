# Next.js adapter deployment e2e

Runs Next.js's official [deployment-adapter compatibility harness][docs]
(`NEXT_TEST_MODE=deploy`) against the Ocel Next adapter. The harness creates an
isolated app per test suite and calls three scripts per app; these are ours:

| script            | harness variable                    | what it does                                                       |
| ----------------- | ----------------------------------- | ------------------------------------------------------------------ |
| `deploy.mjs`      | `NEXT_TEST_DEPLOY_SCRIPT_PATH`      | builds and deploys the temp app, prints **only** the URL to stdout  |
| `logs.mjs`        | `NEXT_TEST_DEPLOY_LOGS_SCRIPT_PATH` | prints the three marker lines, then build, CLI and CloudWatch logs  |
| `cleanup.mjs`     | `NEXT_TEST_CLEANUP_SCRIPT_PATH`     | tears the deployment down, synchronously                            |

`lib.mjs` holds the shared pure logic (unit tested: `pnpm --filter
@ocel-scripts/e2e-next test`). `merge-baseline.mjs` records the known-failure
baseline. `stage-smoke-app.mjs` stages the smoke job's app. `guard-accounts.sh`
refuses to deploy anywhere but the disposable account. The workflow that drives
all of it is `.github/workflows/test-e2e-deploy.yml` — **manual dispatch only**.

`logs.mjs`'s `DEPLOYMENT_ID:` marker carries Ocel's **promotion id** (see
`CONTEXT.md`: an Ocel Deployment is per-app, a Promotion is what one deploy
produces). The marker keeps Next's spelling because the harness parses that
literal; the value comes from `promotionId` in `.ocel/deploy-result.json`.

Each temp app gets its own preview environment *and* its own declared app name
(`e2e-<run id>-<hash of the temp dir>`), so concurrent suites get their own
Cloudflare worker script and S3 asset prefix instead of racing a shared one.

## Jobs, and what the smoke job does not cover

`build` → `smoke` → `test` (16 groups) → `baseline` (recording runs only).

The `smoke` job drives `deploy.mjs`, `logs.mjs` and `cleanup.mjs` **directly**
against one trivial app, because what it asserts — a 200 from the URL plus all
three marker lines — is not observable through `run-tests.js`. Its app is
installed off the `nextjs` checkout by the harness's own
`test/lib/create-next-install`, so it builds against the same Next the matrix
tests; only `react`/`react-dom` come from `smoke-app/package.json`.

The tradeoff: the smoke job does **not** exercise `run-tests.js`,
`NEXT_TEST_MODE=deploy`, or the `NEXT_TEST_*_SCRIPT_PATH` indirection. That
wiring is first exercised by the matrix itself, so a break in it costs one
matrix start rather than being caught by the gate.

## One-time setup (out of band, by a human)

This suite deploys real infrastructure hundreds of times per run. Do all of this
in a **disposable AWS account and Cloudflare account** that hold nothing else.

1. Create a project on the disposable account: run `ocel init` in a scratch
   directory and note the `projectId` and `slug` it writes. Every temp app
   deploys into this one project, as separate preview environments.
2. Give the project a **wildcard preview domain** on a Cloudflare zone in that
   account — `domains: { preview: "*.e2e.example.com" }`. Each preview's DNS
   label lives under it, so without a wildcard the deploys have nowhere to land.
3. Provision the preview substrate once: `ocel bootstrap --preview` (the suite's
   deploys are `ocel preview up`, which refuses if it is missing).
4. Mint an AWS access key and a Cloudflare API token scoped to those accounts,
   and an Ocel access token for the project.

## Secrets and variables the workflow needs

Repository **secrets**:

| name                                | what                                                                 |
| ----------------------------------- | -------------------------------------------------------------------- |
| `E2E_OCEL_ACCESS_TOKEN`             | Ocel access token (`OCEL_ACCESS_TOKEN`; no `ocel login` in CI)        |
| `E2E_AWS_ACCESS_KEY_ID`             | AWS key for the disposable account                                    |
| `E2E_AWS_SECRET_ACCESS_KEY`         | its secret                                                            |
| `E2E_EXPECTED_AWS_ACCOUNT_ID`       | the account id the guard requires the key to resolve to               |
| `E2E_CLOUDFLARE_API_TOKEN`          | Cloudflare API token                                                  |
| `E2E_CLOUDFLARE_ACCOUNT_ID`         | Cloudflare account id passed to the provider                          |
| `E2E_EXPECTED_CLOUDFLARE_ACCOUNT_ID`| the account id the guard requires the token to hold                   |

Repository **variables**:

| name                       | what                                                       |
| -------------------------- | ---------------------------------------------------------- |
| `E2E_OCEL_API_URL`         | Ocel API base URL                                           |
| `E2E_OCEL_PROJECT_ID`      | the shared project's `projectId`                            |
| `E2E_OCEL_PROJECT_SLUG`    | its `slug` (defaults to a sanitized project id if unset)    |
| `E2E_OCEL_PREVIEW_DOMAIN`  | the wildcard preview domain, e.g. `*.e2e.example.com`       |
| `E2E_AWS_REGION`           | region to deploy into                                       |

The expected account ids are deliberately duplicated: the guard compares the
identity the credentials actually resolve to against them and hard-fails on a
mismatch, in the build job and again in every job that deploys. A rotated or
mistyped secret must never spray infrastructure into a real account.

## Recording a baseline

`NEXT_EXTERNAL_TESTS_FILTERS` points the harness at a manifest of known
outcomes; it only fails the job on tests **not** already listed as failing. The
adapter cannot pass everything (edge-runtime suites, for one — the adapter emits
nodejs functions only and skips edge routes), so a baseline is what makes the
matrix a regression signal instead of a wall of red.

The manifest lives in two places, deliberately:

- **`scripts/e2e-next/baseline-manifest.json`** — the committed source of truth
  in this repo, next to the scripts that produce it.
- **`nextjs/test/ocel-deploy-tests-manifest.json`** — where each `test` job
  copies it inside the Next.js checkout. `NEXT_EXTERNAL_TESTS_FILTERS` is
  resolved against the harness's own cwd, so it can only be read from there.

Nothing but the copy step knows about the second path; edit and commit the first.

1. Dispatch the workflow with **`recordBaseline: true`**. The matrix then runs
   unfiltered, emits a results file per suite, and the `baseline` job merges
   every group's fragment.
2. Download the `baseline-manifest` artifact and commit it over
   `scripts/e2e-next/baseline-manifest.json`.
3. Dispatch normally from then on.

A recording run is expensive and may hit AWS Lambda code-storage or Cloudflare
worker-script limits mid-flight; re-dispatch and merge again if it does.

## Promoting newly-passing tests

Newly *added* cases are included automatically — the manifest only ever excludes
what it lists. So when a fix makes a case pass, **delete that case's line from
its suite's `failed` array** in `scripts/e2e-next/baseline-manifest.json` and
commit. The next run will hold the fix in place: if it regresses, the case is no
longer excused and the job fails.

Dropping a whole suite's `"runtimeError": true` entry re-enables the entire file
the same way. Do not re-record a full baseline to promote a fix — that would
silently adopt every *new* failure alongside it.

## Known limits (accepted, not bugs)

- The membrane Lambda layer is **pinned** (`defaultMembraneLayerARN`), so branch
  changes to the cache handler or lambdanode are not exercised, and a baseline is
  only valid for that layer version.
- The multi-GB build cache evicts `build.yml` / `e2e.yml` caches each run.
- Edge-runtime suites cannot pass and land in the baseline wholesale.
- A cancelled or timed-out runner strands that app's Lambdas, worker script and
  DNS label: `cleanup.mjs` is the only footprint control, and it cannot run if
  the runner is killed. Reclaim those with
  `ocel preview rm --name <slug> --yes` (the slug is `e2e-<run id>-…`, printed by
  the job) or `ocel preview ls`.

[docs]: https://nextjs.org/docs/app/api-reference/adapters/testing-adapters
