# Next.js adapter deployment e2e

Runs Next.js's official [deployment-adapter compatibility harness][docs]
(`NEXT_TEST_MODE=deploy`) against the Ocel Next adapter. The harness creates an
isolated app per test suite and calls three scripts per app; these are ours:

| script            | harness variable                    | what it does                                                       |
| ----------------- | ----------------------------------- | ------------------------------------------------------------------ |
| `deploy.mjs`      | `NEXT_TEST_DEPLOY_SCRIPT_PATH`      | builds and deploys the temp app, prints **only** the URL to stdout  |
| `logs.mjs`        | `NEXT_TEST_DEPLOY_LOGS_SCRIPT_PATH` | prints the three marker lines, then build, CLI and CloudWatch logs  |
| `cleanup.mjs`     | `NEXT_TEST_CLEANUP_SCRIPT_PATH`     | removes that app's preview pointer, synchronously                   |

`assert-isr.mjs` is a smoke-job assertion rather than a harness script: it takes
the deployment URL and proves a revalidating route's cache entry is rewritten.
`assert-tag-publisher.mjs`, `assert-suppression-golden.mjs`,
`assert-bytecode.mjs` and `assert-embed.mjs` are the same kind of thing, run
against a deployment URL by hand (see "Golden gate" below).
`assert-bytecode.mjs` proves both legs of the V8 compile cache
`cloud/aws/cmd/lambdanode/bootstrap/bytecode.go` builds — see "What
`assert-bytecode.mjs` proves" below — and `assert-embed.mjs` covers the read leg
in its place when the deploy embedded that cache in the artifact
(`OCEL_BYTECODE_EMBED=1`). Both read slug, environment, app, build id and deploy
time from `.ocel/deploy-result.json` in the deployed app's directory, so run
them from there. `lib.mjs` holds the shared pure logic (unit tested: `pnpm
--filter @ocel-scripts/e2e-next test`); `aws.mjs` holds the `aws` CLI calls the
two assertion scripts share, which is everything that could not be unit tested
and so is kept out of `lib.mjs` rather than left uncovered inside it.
`merge-baseline.mjs` records the known-failure baseline. `stage-smoke-app.mjs`
stages the smoke job's app. `project-teardown.mjs` destroys one whole e2e
project and `sweep-projects.mjs` finds the ones earlier runs stranded (see
"Topology" below); `sidecar.mjs` holds the symlinking both they and `deploy.mjs`
do. `guard-accounts.sh` refuses to deploy anywhere but the disposable account.
The workflow that drives all of it is `.github/workflows/test-e2e-deploy.yml` —
**manual dispatch only**.

`logs.mjs`'s `DEPLOYMENT_ID:` marker carries Ocel's **promotion id** (see
`CONTEXT.md`: an Ocel Deployment is per-app, a Promotion is what one deploy
produces). The marker keeps Next's spelling because the harness parses that
literal; the value comes from `promotionId` in `.ocel/deploy-result.json`.

## Topology: one project per run, one preview per fixture

A whole run deploys into **one Ocel project**, slugged `e2e-<run id>`
(`e2e-local` outside CI), and each temp app is an **ephemeral preview pointer**
inside it: `ocel preview up --ref <NEXT_TEST_DIR>`, torn down with
`ocel preview rm --ref <the same> --yes`. Both identities are derived, pure and
unit-tested (`projectSlug`, `previewRef` in `lib.mjs`), and `deploy.mjs` and
`cleanup.mjs` derive them the same way — a drift between them means cleanup
removes the wrong pointer, or nothing. `deploy.mjs` also persists both to
`.ocel-e2e.json`; the derivation is what recovers a deploy that died before
writing it.

The project is the unit that owns the **preview domain**: declaring
`domains.preview` claims that wildcard outright, and the project gets one
entrypoint worker on one `*.<base>/*` route, created once. So a pointer is a
record in the deployments store — no worker upload, no route creation, nothing
whose propagation a request has to wait on, which is what a project per temp app
paid for in intermittent 522s. What still separates two fixtures is the pointer:
it is their subdomain label, their Pulumi stack, their `ocel:env` tag and their
asset/ISR key prefix (`preview-<pointer>/<slug>/<app>/<build id>`).

The slug is minted locally — `ocel preview up` makes no control-plane call, so a
project needs no `ocel init` or `ocel link`.

**Because the claim is account-wide, two runs cannot share the account.** The
workflow takes a `concurrency` group with `cancel-in-progress: false`: a
cancelled run never reaches its teardown, and the project it strands keeps
holding the wildcard route. `sweep-projects.mjs` is the backstop — see the jobs
below.

## Jobs, and what the smoke job does not cover

`build` → `sweep` → `smoke` → `test` (16 groups) → `destroy`, plus `baseline`
on recording runs only.

`sweep` runs **unconditionally**, before anything is deployed: it enumerates the
SSM path every project's preview root-stack state lives under
(`/ocel/rootstack-preview/`), and destroys every `e2e-*` project that is not
this run's. Anything it finds was stranded by a run that died, and is holding
the domain claim; it is safe to take unconditionally *because* of the
concurrency group. It fails the run when it cannot finish — deploying into a
domain another project still claims fails later and less legibly.

`destroy` is `needs: [smoke, test]`, `if: always()`, and takes the whole project
with `project-teardown.mjs`: the smoke canary's pointer, every pointer a killed
suite never cleaned up, the entrypoint worker, the wildcard route and the
deployments-store instance. Both jobs drive the same script, which renders a
minimal `ocel.config.ts` (slug + provider) into a scratch directory and runs
`ocel destroy --preview --yes` from there — a stranded project has no app
directory left to be addressed from.

The `smoke` job drives `deploy.mjs` and `logs.mjs` **directly** against one
trivial app, because what it asserts — a 200 from the URL, a revalidating
route's cache entry actually being rewritten, plus all three marker lines — is
not observable through `run-tests.js`. Its app is installed off the `nextjs`
checkout by the harness's own `test/lib/create-next-install`, so it builds
against the same Next the matrix tests; only `react`/`react-dom` come from
`smoke-app/package.json`.

It is also what **initializes the project**: its deploy is the first root-stack
reconcile, so every matrix deploy after it finds the entrypoint worker and the
route already there. It does **not** run `cleanup.mjs` — its pointer is kept for
the whole run as a canary, so a URL that stops serving mid-matrix says the
project's entrypoint broke rather than that one fixture did.

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

## What `assert-bytecode.mjs` proves

Bytecode caching is opt-in: without `OCEL_BYTECODE_CACHE=1` on the deploying
process no function is deployed with a bytecode prefix, the warm pass has no
targets, and there is nothing for this script to find. Deploy with the flag set
before running it.

The claim is not "a request eventually leaves a cache behind" but **the cache is
whole before the app serves anyone**. The upload is create-if-absent with no
overwrite and no harvest, and a Next bundle hides many routes behind a
lazily-required entry table, so whichever cold start publishes first fixes that
build's cache for its whole life — organic traffic would fix one route's slice
of it. The deploy therefore invokes each bytecode-gated function once, before
the promote, to load the whole bundle (`cloud/aws/deploy/warm.go`).

So the script asserts, in this order and **before it issues a single request**:

1. The object already exists under the key `bytecode.go` composes (discovered by
   listing: the live node patch version in the key is not knowable ahead of a
   deploy). A successful list that finds nothing is a real miss and fails at
   once — nothing is waited for, since the deploy does not return until every
   warm invocation has answered and each uploads inline.
2. The deploy's own warm summary, read back out of the function's CloudWatch
   logs (`ocel: warm invocation: {…}`), reports `published` with `uploaded:
   true` for that key. That is what **attributes** the object: the PUT is
   create-if-absent, so exactly one writer can create a key and every loser
   answers `already-cached` instead. A summary the script cannot find, or one
   that only ever says `already-cached`, fails — an unattributed object could be
   a one-route cache a stray request got there first with.
3. That summary's `loaded` accounts for all of `entries`, and `skipped` names
   what a stopped walk never reached. A walk the 64 MiB ceiling or the
   invocation deadline cut short is a **warning, not a failure** — it is a real
   outcome for a bundle too big or too slow to warm whole, and the run re-prints
   every warning at the end so a half-warmed bundle can't read as a whole one. A
   summary carrying `uncounted` is the same warning: the membrane published what
   the instance had loaded without node reporting what that was, so the object
   is real and its coverage is unknown.

Then the read leg: a burst of concurrent requests forces fresh sandboxes, and at
least one instance's CloudWatch logs must report a rehydrate hit naming the
discovered key.

What it cannot prove: that the object's *contents* cover every route. It takes
the membrane's own count, gunzips and untars the archive to prove it is a real
nested compile cache, and stops there — mapping cache files back to bundle
entries is node's private business. The whole warm leg also needs a **membrane
layer carrying `warm.go`**; against the pinned `defaultMembraneLayerARN` (see
"Known limits") an older layer answers the warm payload as an empty request,
nothing is published before the promote, and step 1 fails.

## What `assert-embed.mjs` proves, and what it takes off `assert-bytecode.mjs`

`OCEL_BYTECODE_EMBED=1` on the deploying process adds a pass that bakes the
published cache into the function's own artifact
(`.ocel/bytecode/node<ver>-<arch>.tar` inside the `.func` zip,
`cloud/aws/deploy/embed.go`), so a cold start reads it out of `/var/task`
instead of paying an S3 GET. **The two scripts split along that flag:**

- With the flag off, `assert-embed.mjs` skips and asserts nothing, loudly.
- With the flag on, `assert-bytecode.mjs` drops its read leg — the S3 rehydrate
  line it requires is one embedding makes *false*, not merely unlikely — and
  says so as an end-of-run warning naming the leg as unproven. Its write leg is
  unaffected and still runs: the embed pass reads what the warm pass published,
  so the object and its warm summary have to exist either way.

So under the flag the two are complementary and **both must run** for the read
leg to be covered at all. `assert-embed.mjs` asserts, in order:

1. The function's `CodeSha256` is not the sha of the artifact the deploy
   originally uploaded. The pass writes the merged bundle to a new
   content-addressed key beside the original (`<hash>-bc-<digest>.zip`) rather
   than over it, so both objects are in the artifact bucket afterwards and the
   pair is discoverable by listing — the deploy tells nothing outside it either
   key. Equal shas mean the repackaged bundle was built and uploaded but
   `UpdateFunctionCode` never moved the function onto it.
2. The package Lambda actually holds — downloaded from the presigned
   `Code.Location`, and required to hash to the reported `CodeSha256` before
   anything is read out of it — carries `.ocel/bytecode/node<ver>-<arch>.tar` at
   exactly the name derived from the cache key an independent S3 listing found.
   Deriving it from the key is the point: both the deploy and the membrane
   derive that path from the key's basename, and a tar under any other name is
   one no cold start will ever look for. A package carrying some *other* entry
   under `.ocel/bytecode/` is reported as that distinct failure.
3. A burst of cold starts logs `loaded embedded compile cache from` and **never**
   `rehydrated compile cache from`. The poll runs the full window rather than
   stopping at the first embedded hit: one hit proves an instance read the
   artifact, but "none fell through to S3" is a claim about every instance the
   burst started. A window in which no log read ever succeeded fails as
   *unreadable*, separately and before the absence of the S3 line can be
   mistaken for proof of it.

Every way the embedded path can fail degrades to the S3 behaviour
`assert-bytecode.mjs` already proves, so the deployment stays green and nothing
gets slower — which is exactly why this needs its own assertion rather than
being visible in a response.

## Golden gate: `purpose: prefetch` has no side effect

The edge stamps `purpose: prefetch` on the user-path forward to the Lambda so
Next serves a stale entry without starting its own render (bd ocelhq-wvag.26) —
the edge's admission tiers are then the only thing that can start one. That rests
on one line of `next@16.2.10`, and OpenNext's caveat about it comes with it: a
change to Next's prefetch handling would break it. `SUPPRESS_SELF_REVALIDATION`
in `platform/edge/cloudflare/workers/entry/src/cache.ts` is the one-line revert — it gates both the stamp
and the colo tier's refusal to store the stale serve it produces;
`assert-suppression-golden.mjs` is the tripwire.

It fetches `smoke-app/app/golden/page.tsx` — a prerender whose body carries
nothing per-render — twice per variant (html and RSC), both down the BYPASS path
so the edge handles them identically and the only difference reaching the Lambda
is the header. Status, body bytes and headers must match, modulo the set in
`GOLDEN_VOLATILE_HEADERS` (`date`/`age`, `x-nextjs-cache`, `x-ocel-cache`,
`x-vercel-cache`, and the Cloudflare/transport headers), each excluded for a
reason named at the constant.

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

## The `x-vercel-cache` alias: `OCEL_E2E_VERCEL_CACHE_HEADER`

Next's deploy suites (`cache-components-prerender-matrix` and friends) assert
Vercel's `x-vercel-cache` header to tell which tier answered. `deploy.mjs` always
sets `OCEL_E2E_VERCEL_CACHE_HEADER=1` on the build it runs; the Next adapter reads
it once and records `vercelCacheAlias: true` in `routing-manifest.json`, and the
edge worker stamps `x-vercel-cache` as a verbatim copy of `x-ocel-cache` on the
way out — never into a stored cache entry, and never on a response that carries
no `x-ocel-cache`.

Both halves are independently safe and production must set neither: a build
without the variable emits no manifest field, and a worker whose manifest lacks
the field emits no alias. Nothing outside this harness sets it (bd ocelhq-6l0y).

## No Cloudflare observability: `OCEL_EDGE_OBSERVABILITY`

Cloudflare bills Workers logs and traces per event, and one run of this suite
serves hundreds of deployments through its entrypoint worker, whose output
nobody ever reads. `deploy.mjs` always sets `OCEL_EDGE_OBSERVABILITY=off`, and
`cloud/edge/cloudflare` then uploads every script — the project's entrypoint
worker and the account-level deployments-store and isr-writer workers alike —
with `observability.enabled: false`.

So there is nothing to look at in the Cloudflare dashboard for a failing suite.
Debug from `deploy-build.log` in the temp app and from `logs.mjs`, which replays
it. To get the dashboard back for one investigation, unset the variable and
redeploy: the disable is uploaded explicitly, so the next upload turns it back
on rather than leaving the script with whatever it last had.

## One-time setup (out of band, by a human)

This suite deploys real infrastructure hundreds of times per run. Do all of this
in a **disposable AWS account and Cloudflare account** that hold nothing else.

No project is created by hand — each run mints its own — but the zone it lands
on has to be prepared:

1. Pick a **Cloudflare zone** in that account and give the previews their
   **wildcard hostname**: the `E2E_OCEL_PREVIEW_DOMAIN` value itself, e.g.
   `*.ocel.site` (`*.e2e.example.com` for a subdomain zone). Keep it a single
   wildcard level — Cloudflare universal SSL does not cover a nested one, so
   `*.<run id>.ocel.site` is not an option. A run's first preview deploy plants
   a **proxied** placeholder record for it and binds the project's entrypoint
   worker to `*.<base>/*`; a record you made yourself is left alone, but it must
   be **proxied — orange cloud**, since an unproxied hostname never reaches a
   worker.
2. Provision the preview substrate once — it is account-global, not per-project:
   from a scratch directory holding an `ocel.config.ts` that declares the AWS
   provider, run `ocel bootstrap --preview` (the suite's deploys are
   `ocel preview up`, which refuses if it is missing).
3. Create the **AWS role the workflow assumes** — no access key is stored. It
   needs a GitHub OIDC trust policy (provider
   `token.actions.githubusercontent.com`, audience `sts.amazonaws.com`, subject
   scoped to this repo) and a **`MaxSessionDuration` of at least 21600** (6h).
   The `test` job mints one 6h session at its start and never refreshes it: the
   deploy path reads credentials off the default chain and has nowhere to go
   back to for new ones, so a shorter maximum fails the assume immediately —
   which is the failure to want, since expiry mid-run strands deployed apps.
4. Mint a Cloudflare API token scoped to that account, and an Ocel access token.

## Secrets and variables the workflow needs

Repository **secrets**:

| name                                | what                                                                 |
| ----------------------------------- | -------------------------------------------------------------------- |
| `E2E_OCEL_ACCESS_TOKEN`             | Ocel access token (`OCEL_ACCESS_TOKEN`; no `ocel login` in CI)        |
| `E2E_AWS_ROLE_ARN`                  | the role each AWS-touching job assumes over OIDC                      |
| `E2E_EXPECTED_AWS_ACCOUNT_ID`       | the account id the guard requires the session to resolve to           |
| `E2E_CLOUDFLARE_API_TOKEN`          | Cloudflare API token                                                  |
| `E2E_CLOUDFLARE_ACCOUNT_ID`         | Cloudflare account id passed to the provider                          |
| `E2E_EXPECTED_CLOUDFLARE_ACCOUNT_ID`| the account id the guard requires the token to hold                   |
| `TURBO_TOKEN`                       | Vercel access token for turbo's remote cache (optional; see below)     |

Repository **variables**:

| name                       | what                                                       |
| -------------------------- | ---------------------------------------------------------- |
| `E2E_OCEL_API_URL`         | Ocel API base URL                                           |
| `E2E_OCEL_PREVIEW_DOMAIN`  | the wildcard preview domain, e.g. `*.e2e.example.com` — the proxied record from step 1 |
| `E2E_AWS_REGION`           | region to deploy into                                       |
| `TURBO_TEAM`               | Vercel team slug (or username on a personal account) for the remote cache (optional) |

`TURBO_TOKEN`/`TURBO_TEAM` are the only optional pair: Next.js builds itself with
turbo (`turbo run build`), and its build is the one part of the build job that is
the same from run to run — the job's own `actions/cache` is keyed by run id, so it
ships artifacts between a single run's jobs and never across runs. With both set,
that build is restored from Vercel's remote cache on every run pinned to the same
`nextjsRef`; with either missing, turbo builds locally exactly as it did before.
Nothing inside the `vercel/next.js` checkout is modified for this — turbo reads
both from the environment.

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
for pkg in ocel @ocel/linux-x64 @ocel/provider-aws @ocel/provider-aws-linux-x64; do
  pnpm --filter "$pkg" exec pnpm pack --pack-destination "$TARBALLS"
done
cd "$SIDECAR" && npm init -y >/dev/null
npm install --no-audit --no-fund "$TARBALLS"/*.tgz
test -d node_modules/ocel && test -x node_modules/@ocel/provider-aws-linux-x64/bin/deploy
```

`@ocel/linux-x64` carries the CLI binary the worker bundle and the Next adapter
are embedded in, and it is an optionalDependency of `ocel` — but `deploy.mjs`
runs the adapter repo's own `packages/ocel/bin/run.js`, never the sidecar's, so
this list controls only which CLI the sidecar's `ocel` would launch if
something ran it directly. A worker-source or Next-adapter change needs a
rebuild of that binary in the adapter repo, not a sidecar repack:

```bash
node scripts/build-native.mjs --host --target cli
```

The sidecar only needs repacking when `ocel/config` resolution or the
`@ocel/provider-aws*` binaries change.

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
   unfiltered, and the `baseline` job merges every group's fragment.

   Each group's fragment is built from the harness's own stdout, tee'd to
   `$RUNNER_TEMP`, rather than the `.results.json` files it leaves in the
   checkout. Between retries of a failing suite the harness runs `git clean -fdx`
   on that suite's directory, and for a top-level `test/e2e/<name>.test.ts` that
   directory is the whole of `test/e2e` — which deletes every other suite's
   results file. `collect` fails the job if any suite the harness started has no
   result, so a group can never silently record a fraction of its run.
2. Download the `baseline-manifest` artifact and commit it over
   `scripts/e2e-next/baseline-manifest.json`.
3. Dispatch normally from then on.

A recording run is expensive and may hit AWS Lambda code-storage or Cloudflare
worker-script limits mid-flight; re-dispatch and merge again if it does.

## Promoting newly-passing tests

Newly *added* cases are included automatically — the manifest only ever excludes
what it lists, and a suite it does not list runs in full. So when a fix makes a
case pass, **delete that case's line from its suite's `failed` array** in
`scripts/e2e-next/baseline-manifest.json` and commit. The next run will hold the
fix in place: if it regresses, the case is no longer excused and the job fails.
Delete the suite's whole entry once its `failed` array is empty.

`test/get-test-filter.js` reads only `runtimeError`, `failed` and `flakey`, so
those are the only fields recorded — there is no `passed` list to maintain, and a
green suite is simply absent. The manifest is the outstanding-work list; it is
empty when the adapter is green.

Dropping a whole suite's `"runtimeError": true` entry re-enables the entire file
the same way. Do not re-record a full baseline to promote a fix — that would
silently adopt every *new* failure alongside it.

## What `deploy.mjs` writes into the temp app before installing

The harness hands over a fixture directory, not a reproducible install: it
carries no lockfile, and its `package.json` may declare `"typescript": "latest"`.
So `deploy.mjs` patches `package.json` **before** `ensureDeps()` runs any
install, via `withPinnedTypeScript` (pure, unit-tested in `lib.test.mjs`):

- `typescript` is pinned to `^5` in `dependencies`/`devDependencies`, and
  declared as a devDependency for a fixture that has none. typescript@7 is the
  Go-native rewrite and ships no `lib/typescript.js`, which is the file Next's
  `has-necessary-dependencies` probes for; without it Next concludes TypeScript
  is missing, auto-installs `latest` (7 again), and calls `require(undefined)`.
  Declaring it means that auto-install never runs.
- The same pin goes into both `overrides` and `pnpm.overrides`, covering
  transitive resolutions and whichever package manager Next would have detected.

Separately, an app with a `next.config.ts`/`.mts` is built with
`__NEXT_NODE_NATIVE_TS_LOADER_ENABLED=true`. Without it Next transpiles the
config to commonjs and `require`s it from a string, which cannot load a config
containing top-level await. It reaches `next build` by inheritance: the Go CLI
composes the builder's environment from `os.Environ()`, and `buildNext` spawns
the fixture's build script under `process.env`.

## Known limits (accepted, not bugs)

- The membrane Lambda layer is **pinned** (`defaultMembraneLayerARN`), so branch
  changes to the cache handler or lambdanode are not exercised, and a baseline is
  only valid for that layer version.
- The multi-GB build cache evicts `build.yml` / `e2e.yml` caches each run.
- Edge-runtime suites cannot pass and land in the baseline wholesale.
- A cancelled or timed-out runner strands whatever that job had deployed:
  `cleanup.mjs` and the `destroy` job both need a live runner. The next run's
  `sweep` job reclaims it, so nothing accumulates — but until then the stranded
  project holds the preview domain's wildcard route, which is an account-wide
  claim. Reclaim one by hand with
  `node scripts/e2e-next/project-teardown.mjs e2e-<run id>` (with `ADAPTER_DIR`
  and `OCEL_E2E_SIDECAR_DIR` set); `ocel destroy --preview` addresses the
  project through the config in its working directory, which is why that script
  renders one, and why `cleanup.mjs` re-renders the app's if it is gone.

[docs]: https://nextjs.org/docs/app/api-reference/adapters/testing-adapters
