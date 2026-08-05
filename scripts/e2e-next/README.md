# Next.js adapter deployment e2e

Runs Next.js's official [deployment-adapter compatibility harness][docs]
(`NEXT_TEST_MODE=deploy`) against the Ocel Next adapter. The harness creates an
isolated app per test suite and calls three scripts per app; these are ours:

| script            | harness variable                    | what it does                                                       |
| ----------------- | ----------------------------------- | ------------------------------------------------------------------ |
| `deploy.mjs`      | `NEXT_TEST_DEPLOY_SCRIPT_PATH`      | builds and deploys the temp app, prints **only** the URL to stdout  |
| `logs.mjs`        | `NEXT_TEST_DEPLOY_LOGS_SCRIPT_PATH` | prints the three marker lines, then build, CLI and CloudWatch logs  |
| `cleanup.mjs`     | `NEXT_TEST_CLEANUP_SCRIPT_PATH`     | tears the deployment down, synchronously                            |

`assert-isr.mjs` is a smoke-job assertion rather than a harness script: it takes
the deployment URL and proves a revalidating route's cache entry is rewritten.
`assert-tag-publisher.mjs` and `assert-suppression-golden.mjs` are the same kind
of thing, run against a deployment URL by hand (see "Golden gate" below).
`lib.mjs` holds the shared pure logic (unit tested: `pnpm --filter
@ocel-scripts/e2e-next test`). `merge-baseline.mjs` records the known-failure
baseline. `stage-smoke-app.mjs` stages the smoke job's app. `guard-accounts.sh`
refuses to deploy anywhere but the disposable account. The workflow that drives
all of it is `.github/workflows/test-e2e-deploy.yml` — **manual dispatch only**.

`logs.mjs`'s `DEPLOYMENT_ID:` marker carries Ocel's **promotion id** (see
`CONTEXT.md`: an Ocel Deployment is per-app, a Promotion is what one deploy
produces). The marker keeps Next's spelling because the harness parses that
literal; the value comes from `promotionId` in `.ocel/deploy-result.json`.

Each temp app gets its own **Ocel project**, slugged `e2e-<run id>-<hash of the
temp dir>` and deployed as a single persistent preview inside it. The project
namespaces the Pulumi stacks, the deployments store, the asset prefixes and the
Cloudflare worker scripts and routes, so the 32 deploys a run has in flight at
once share no infrastructure and cannot steal each other's hostnames. The slug is
minted locally — `ocel preview up` makes no control-plane call, so a project
needs no `ocel init` or `ocel link` — and derived, so `cleanup.mjs` can re-derive
it from `GITHUB_RUN_ID` and `NEXT_TEST_DIR` alone when a deploy left no state
behind.

## Jobs, and what the smoke job does not cover

`build` → `smoke` → `test` (16 groups) → `baseline` (recording runs only).

The `smoke` job drives `deploy.mjs`, `logs.mjs` and `cleanup.mjs` **directly**
against one trivial app, because what it asserts — a 200 from the URL, a
revalidating route's cache entry actually being rewritten, plus all three marker
lines — is not observable through `run-tests.js`. Its app is installed off the
`nextjs` checkout by the harness's own `test/lib/create-next-install`, so it
builds against the same Next the matrix tests; only `react`/`react-dom` come
from `smoke-app/package.json`.

`assert-isr.mjs` is the revalidation half of that. Serving a 200 proves nothing
about ISR: the worker gates a tier's self-refresh on `revalidates =
!edgeEntryKey`, and a wrong value there leaves every route answering correctly
with only revalidation dead. The probe route (`smoke-app/app/isr/page.tsx`,
`revalidate = 5`) renders a fresh token per render, so the assertion waits out
the window and requires the token to change **on a cached tier** —
`x-ocel-cache` of `HIT`/`PRERENDER`/`STALE`. A change on a `MISS` is reported as
a distinct failure: that is a fresh render with nothing stored.

The tradeoff: the smoke job does **not** exercise `run-tests.js`,
`NEXT_TEST_MODE=deploy`, or the `NEXT_TEST_*_SCRIPT_PATH` indirection. That
wiring is first exercised by the matrix itself, so a break in it costs one
matrix start rather than being caught by the gate.

## Golden gate: `purpose: prefetch` has no side effect

The edge stamps `purpose: prefetch` on the user-path forward to the Lambda so
Next serves a stale entry without starting its own render (bd ocelhq-wvag.26) —
the edge's admission tiers are then the only thing that can start one. That rests
on one line of `next@16.2.10`, and OpenNext's caveat about it comes with it: a
change to Next's prefetch handling would break it. `SUPPRESS_SELF_REVALIDATION`
in `workers/nextjs/src/cache.ts` is the one-line revert — it gates both the stamp
and the colo tier's refusal to store the stale serve it produces;
`assert-suppression-golden.mjs` is the tripwire.

It fetches `smoke-app/app/golden/page.tsx` — a prerender whose body carries
nothing per-render — twice per variant (html and RSC), both down the BYPASS path
so the edge handles them identically and the only difference reaching the Lambda
is the header. Status, body bytes and headers must match, modulo the set in
`GOLDEN_VOLATILE_HEADERS` (`date`/`age`, `x-nextjs-cache`, `x-ocel-cache`, and
the Cloudflare/transport headers), each excluded for a reason named at the
constant.

Each pair is preceded by a wait past the probe page's own short `revalidate`, so
both legs are answered from a STALE entry, and a pair where neither leg reports
one fails. `purpose` is read beside a first operand that short-circuits on a
fresh entry, so a comparison made against a freshly warmed page proves only that
the header does not change a fresh serve.

```bash
node scripts/e2e-next/assert-suppression-golden.mjs "$SMOKE_URL"
```

Not wired into the workflow: like `assert-tag-publisher.mjs`, it is run by hand
against a deployment URL. Its pure half is covered by
`pnpm --filter @ocel-scripts/e2e-next test`, which proves the comparison, not the
Lambda — only a run against a real deployment does that.

## One-time setup (out of band, by a human)

This suite deploys real infrastructure hundreds of times per run. Do all of this
in a **disposable AWS account and Cloudflare account** that hold nothing else.

No project is created by hand — each deployed test app mints its own — but the
zone it lands on has to be prepared:

1. Pick a **Cloudflare zone** in that account and create the **wildcard DNS
   record** the previews resolve through: the `E2E_OCEL_PREVIEW_DOMAIN` value
   itself, e.g. `*.ocel.site` (`*.e2e.example.com` for a subdomain zone). It must
   be **proxied — orange cloud**: an unproxied hostname never reaches a worker.
   Deploys only *verify* this record; they no longer create per-hostname DNS, so a
   record that is missing or grey-clouded fails the deploy with an actionable
   error rather than being fixed for you. Create it once, by hand.
2. Provision the preview substrate once — it is account-global, not per-project:
   from a scratch directory holding an `ocel.config.ts` that declares the AWS
   provider, run `ocel bootstrap --preview` (the suite's deploys are
   `ocel preview up`, which refuses if it is missing).
3. Mint an AWS access key and a Cloudflare API token scoped to those accounts,
   and an Ocel access token.

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
| `E2E_OCEL_PREVIEW_DOMAIN`  | the wildcard preview domain, e.g. `*.e2e.example.com` — the proxied record from step 1 |
| `E2E_AWS_REGION`           | region to deploy into                                       |

The expected account ids are deliberately duplicated: the guard compares the
identity the credentials actually resolve to against them and hard-fails on a
mismatch, in the build job and again in every job that deploys. A rotated or
mistyped secret must never spray infrastructure into a real account.

## Repacking the sidecar

The sidecar is the only thing a temp app sees of Ocel: `deploy.mjs` symlinks both
`<temp-app>/node_modules/ocel` and `<temp-app>/node_modules/@ocel` at it. CI
builds one per run (`test-e2e-deploy.yml`); local runs reuse a long-lived one
pointed at by `OCEL_E2E_SIDECAR_DIR`.

A sidecar packed before `@ocel/sdk` folded into the root `ocel` package has no
`node_modules/ocel`, and `linkSidecar` now fails on it rather than deploying an
app whose `ocel.config.ts` cannot resolve `ocel/config`. Repack it once:

```bash
SIDECAR=<sidecar dir>
TARBALLS=$(mktemp -d)
cd <adapter repo> && pnpm --filter ocel build
for pkg in ocel @ocel/provider-aws @ocel/provider-aws-linux-x64; do
  pnpm --filter "$pkg" exec pnpm pack --pack-destination "$TARBALLS"
done
cd "$SIDECAR" && npm init -y >/dev/null
npm install --no-audit --no-fund "$TARBALLS"/*.tgz
test -d node_modules/ocel && test -x node_modules/@ocel/provider-aws-linux-x64/bin/deploy
```

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
- A cancelled or timed-out runner strands that app's whole project — Lambdas,
  worker scripts, deployments store and DNS label: `cleanup.mjs` is the only
  footprint control, and it cannot run if the runner is killed. Reclaim one by
  running `ocel preview rm --name <slug> --yes` from a directory whose
  `ocel.config.ts` declares that `slug` (it is `e2e-<run id>-…`, printed by the
  job); `preview rm` addresses the project through the config in its working
  directory, which is why `cleanup.mjs` re-renders the config if it is gone.

[docs]: https://nextjs.org/docs/app/api-reference/adapters/testing-adapters
