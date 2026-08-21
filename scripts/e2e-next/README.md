# Next.js adapter deployment e2e

Runs Next.js's official [deployment-adapter compatibility harness][docs]
(`NEXT_TEST_MODE=deploy`) against the Ocel Next adapter, deploying real
infrastructure hundreds of times per run.

`.github/workflows/test-e2e-deploy.yml` drives it and is the source of truth for
what runs. **Manual dispatch only.** Inputs: `nextjsRef` (must match the `next`
version the adapter pins) and `recordBaseline`.

Read the scripts for what they do. This file covers only what you cannot get by
running them.

## One-time setup (out of band, by a human)

Use a **disposable AWS account and Cloudflare account** that hold nothing else.

No project is created by hand — each run mints its own — but the zone must be
prepared:

1. Pick a **Cloudflare zone** and give the previews their wildcard hostname,
   e.g. `*.ocel.site`. Keep it a **single** wildcard level — Cloudflare universal
   SSL does not cover a nested one, so `*.<run id>.ocel.site` is not an option.
   `ocel domain use` plants a proxied placeholder record and binds the shared
   entry worker; a record you made yourself is left alone but must be **proxied
   (orange cloud)**, since an unproxied hostname never reaches a worker.
2. Provision the preview bootstrap once and give it the wildcard — both are
   account-global, not per-project. From a scratch directory holding an
   `ocel.config.ts` that declares the AWS provider:
   ```bash
   ocel bootstrap --preview --features all
   ocel domain use '*.ocel.site' --preview
   ```
   No project declares a preview domain of its own; every run's previews serve
   on this one, at `<slug>--<ref>.ocel.site`.
3. Create the **AWS role the workflow assumes** — no access key is stored. It
   needs a GitHub OIDC trust policy (provider
   `token.actions.githubusercontent.com`, audience `sts.amazonaws.com`, subject
   scoped to this repo) and a **`MaxSessionDuration` of at least 21600** (6h).
   The `test` job mints one 6h session and never refreshes it, so a shorter
   maximum fails the assume immediately — the failure to want, since expiry
   mid-run strands deployed apps.
4. Mint a Cloudflare API token scoped to that account, and an Ocel access token.

### Secrets

| name                                 | what                                                          |
| ------------------------------------ | ------------------------------------------------------------- |
| `E2E_OCEL_ACCESS_TOKEN`              | Ocel access token (no `ocel login` in CI)                      |
| `E2E_AWS_ROLE_ARN`                   | the role each AWS-touching job assumes over OIDC               |
| `E2E_EXPECTED_AWS_ACCOUNT_ID`        | the account id the guard requires the session to resolve to    |
| `E2E_CLOUDFLARE_API_TOKEN`           | Cloudflare API token                                           |
| `E2E_CLOUDFLARE_ACCOUNT_ID`          | Cloudflare account id passed to the provider                   |
| `E2E_EXPECTED_CLOUDFLARE_ACCOUNT_ID` | the account id the guard requires the token to hold            |
| `TURBO_TOKEN`                        | Vercel token for turbo's remote cache (optional, with `TURBO_TEAM`) |

### Variables

| name                      | what                                                      |
| ------------------------- | --------------------------------------------------------- |
| `E2E_OCEL_API_URL`        | Ocel API base URL                                          |
| `E2E_AWS_REGION`          | region to deploy into                                      |
| `E2E_PREVIEW_DOMAIN`      | the preview wildcard, e.g. `*.ocel.site`; each run reinstalls the shared entry worker on it |
| `TURBO_TEAM`              | Vercel team slug for the remote cache (optional)           |

The expected account ids are deliberately duplicated: the guard compares the
identity the credentials actually resolve to against them and hard-fails on a
mismatch. A rotated or mistyped secret must never spray infrastructure into a
real account.

## Recording a baseline

`NEXT_EXTERNAL_TESTS_FILTERS` points the harness at a manifest of known
outcomes; it fails the job only on tests **not** already listed as failing. The
adapter cannot pass everything, so a baseline is what makes the matrix a
regression signal instead of a wall of red.

The manifest lives in two places: **`baseline-manifest.json`** here is the
committed source of truth — edit and commit this one. Each `test` job copies it
to `nextjs/test/ocel-deploy-tests-manifest.json` inside the Next.js checkout,
because `NEXT_EXTERNAL_TESTS_FILTERS` resolves against the harness's own cwd.

1. Dispatch with **`recordBaseline: true`**. The matrix runs unfiltered and the
   `baseline` job merges every group's fragment.
2. Download the `baseline-manifest` artifact and commit it over
   `baseline-manifest.json`.
3. Dispatch normally from then on.

A recording run is expensive and may hit AWS Lambda code-storage or Cloudflare
worker-script limits mid-flight; re-dispatch and merge again if it does.

## Promoting newly-passing tests

Newly *added* cases are included automatically — the manifest only ever excludes
what it lists. When a fix makes a case pass, **delete that case's line from its
suite's `failed` array** and commit; the next run holds the fix in place. Delete
the suite's whole entry once its `failed` array is empty, and drop a
`"runtimeError": true` entry to re-enable a whole file.

Only `runtimeError`, `failed` and `flakey` are read, so there is no `passed`
list to maintain and a green suite is simply absent. The manifest is the
outstanding-work list; it is empty when the adapter is green.

**Do not re-record a full baseline to promote a fix** — that silently adopts
every new failure alongside it.

## Assertions run by hand

Not wired into the workflow. Run each against a real deployment; their pure
halves are covered by `pnpm --filter @ocel-scripts/e2e-next test`, which proves
the comparison and not the Lambda.

| script                          | how to run                                                    |
| ------------------------------- | ------------------------------------------------------------- |
| `assert-suppression-golden.mjs` | `node …/assert-suppression-golden.mjs "$SMOKE_URL"`            |
| `assert-tag-publisher.mjs`      | same, with a deployment URL                                    |
| `assert-bytecode.mjs`           | from the deployed app's directory; deploy with `OCEL_BYTECODE_CACHE=1` first, or it has nothing to find |
| `assert-embed.mjs`              | same, and additionally `OCEL_BYTECODE_EMBED=1`                 |

Under `OCEL_BYTECODE_EMBED=1` the last two are complementary and **both must
run**: embedding makes the S3 rehydrate line `assert-bytecode.mjs` looks for
false, so it drops that leg and warns, and `assert-embed.mjs` covers it instead.
Both read slug, environment, app, build id and deploy time from
`.ocel/deploy-result.json` in the deployed app's directory.

## Repacking the sidecar

The sidecar is the only thing a temp app sees of Ocel. CI builds one per run;
local runs reuse a long-lived one at `OCEL_E2E_SIDECAR_DIR`. It needs repacking
only when `ocel/config` resolution or the `@ocel/provider-aws*` binaries change.

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

A worker-source or Next-adapter change needs a rebuild of the CLI binary in the
adapter repo instead, not a sidecar repack:

```bash
node scripts/build-native.mjs --host --target cli
```

## Reclaiming a stranded project

A cancelled or timed-out runner strands whatever that job deployed — cleanup and
the `destroy` job both need a live runner. The next run's `sweep` job reclaims
it, so nothing accumulates, but until then its store instance, staged
deployments and assets keep billing, and its slug stays taken.

The wildcard itself is held by the shared entry worker, not by any project, so a
stranded run no longer blocks another from deploying previews.

```bash
ADAPTER_DIR=… OCEL_E2E_SIDECAR_DIR=… \
  node scripts/e2e-next/project-teardown.mjs e2e-<run id>
```

## Debugging a failing suite

Workers observability is uploaded **off** — one run serves hundreds of
deployments through the entrypoint worker and Cloudflare bills logs per event —
so there is nothing to see in the Cloudflare dashboard. Debug from
`deploy-build.log` in the temp app, and from `logs.mjs`, which replays it. To
get the dashboard back for one investigation, unset `OCEL_EDGE_OBSERVABILITY`
and redeploy; the disable is uploaded explicitly, so the next upload turns it
back on.

[docs]: https://nextjs.org/docs/app/api-reference/adapters/testing-adapters
