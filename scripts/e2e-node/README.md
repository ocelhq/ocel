# Node-framework deployment e2e

Deploys an express app and a hono app into a real AWS account behind a real
Cloudflare zone, then proves each one answers a real request **through the edge
worker**. That last clause is the whole suite: an app with no worker is still
announced in `appUrls`, as the Lambda Function URL the deploy falls back to, and
that URL is created with `AuthorizationType: IAM` — it 403s every unsigned
request, so a deploy can report success while the app is unreachable. That is
how non-next deploys stayed broken.

Read the scripts for what they do. This file covers only what you cannot get by
running them.

**Manual only.** No workflow drives it; every step below needs credentials, and
credentials are the human's to hand over.

## One-time setup (out of band, by a human)

The same disposable AWS and Cloudflare accounts and the same preview substrate
that `scripts/e2e-next/README.md` describes — read its "One-time setup" and do
that. This suite adds nothing to it. It does **not** share the next suite's
project: slugs here are prefixed `e2en-` where the next suite's are `e2e-`, so
the two can run at the same time against one substrate and one wildcard.

Environment the scripts read:

| name                        | what                                                      |
| --------------------------- | --------------------------------------------------------- |
| `ADAPTER_DIR`               | this repo's root — the toolchain is built here and the staged app links into it |
| `AWS_*` / `AWS_PROFILE`     | credentials for the disposable account                     |
| `CLOUDFLARE_API_TOKEN`      | token for the disposable Cloudflare account                |
| `CLOUDFLARE_ACCOUNT_ID`     | account the provider uploads workers to                    |
| `OCEL_ACCESS_TOKEN`         | Ocel access token, in place of `ocel login`                |
| `OCEL_API_URL`              | Ocel API base URL                                          |
| `GITHUB_RUN_ID`             | optional; names the project. Unset means `e2en-local`      |

Before deploying anything, prove the credentials point where you think they do:

```bash
EXPECTED_AWS_ACCOUNT_ID=… EXPECTED_CLOUDFLARE_ACCOUNT_ID=… \
  scripts/e2e-node/guard-accounts.sh
```

## Running it

```bash
export ADAPTER_DIR=$PWD
STAGED=$(node scripts/e2e-node/stage-smoke-app.mjs)
cd "$STAGED"
node "$ADAPTER_DIR"/scripts/e2e-node/deploy.mjs
node "$ADAPTER_DIR"/scripts/e2e-node/assert-serves.mjs
node "$ADAPTER_DIR"/scripts/e2e-node/cleanup.mjs
```

`deploy.mjs` prints one line per app — its name, its framework and the URL the
deploy announced. `assert-serves.mjs` is the judgement; run it from the same
directory, since it reads `.ocel/deploy-result.json` and `.ocel-e2e-node.json`
there. `cleanup.mjs` must run even when the assertions fail, or the preview
keeps billing.

`deploy.mjs` **builds the toolchain first** — the ocel package's dist, the CLI
binary that embeds the node builder, and the provider's `deploy` and `runtime`
binaries. It does that because a suite that deploys a stale binary tests the old
behaviour and reports a pass. `OCEL_E2E_NODE_SKIP_BUILD=1` skips the build for a
fast second iteration; it still refuses to run if any of the four artifacts is
absent, and it says loudly that it trusted what was already there.

## What `assert-serves.mjs` proves

Per app, in order, stopping at the first thing that fails:

1. The app has a URL at **its own** preview hostname. Attribution is by
   hostname label, never by position in `appUrls` — an app that got no worker
   contributes no entry and would otherwise shift every later app's URL.
2. Nothing is left over in `appUrls`. A leftover is the Function URL fallback.
3. `/` answers 200 with a `cf-ray` header, so Cloudflare handled it, and its
   body carries the app's marker, so the bytes came from the deployed Lambda.
4. `/health` answers JSON naming the framework, so the right app answered.
5. A `POST` to a deep `/echo/…` path round-trips method, path, query string,
   a request header and a JSON body intact.
6. A route that sets `418` reaches the client as `418`.
7. The Lambda's own Function URL, unsigned, answers **403**. This is the leg
   that makes the 200 above mean something: if the origin is closed to everyone
   without a signature, and the signer only exists in the worker, then the 200
   came through the worker.

Step 7 needs the `aws` CLI and credentials. Without them it is **skipped, not
passed** — the script says so per app and again in its summary line. Every other
step needs only the deployment.

The pure halves — slug and pointer derivation, hostname attribution, the edge
verdict, the echo comparison — are covered by
`pnpm --filter @ocel-scripts/e2e-node test`, which proves the comparisons and
not the Lambda.

## Reclaiming a stranded project

A cancelled run strands whatever it deployed; teardown needs a live process.

```bash
ADAPTER_DIR=… node scripts/e2e-node/project-teardown.mjs e2en-<run id>
```

`sweep-projects.mjs` does the same for every `e2en-` project except the current
run's, reading the substrate's root-stack parameters to find them. It never
touches the next suite's `e2e-` projects.

## Adding a framework

`SMOKE_APPS` in `lib.mjs` is the list. An entry needs a directory under
`smoke-app/` holding a `package.json` that depends on the framework and a
`src/server.ts` serving `/`, `/health`, `/status/:code` and `/echo/*` the way
the two existing ones do — `lib.test.mjs` checks the marker and the dependency,
and `previewLabelProblem` will tell you if the name pushes a preview hostname
past 63 characters.
