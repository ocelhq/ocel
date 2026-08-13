# Deploy benchmark matrix

Deploys the same node app to real AWS through several tools and measures what a
user actually gets: how long the deploy takes, how slow a cold start is, and
what Lambda bills for a request. One cell is one (app x platform) pair; every
cell is deployed, measured and torn down before the next begins, because
concurrent deploys contend and poison the latency numbers.

Read the scripts for what they do. This file covers only what you cannot get by
running them.

**Manual only.** No workflow drives it. Every cell spends real money in a real
account, and the credentials are the human's to hand over.

## The two axes

`matrix.config.mjs` is the whole matrix and the only file you edit to change it.

Apps live in `apps/<framework>/`. Each has `src/app.ts` holding the routes,
`src/server.ts` which only calls `listen()`, and `src/handler.ts` which wraps the
same app in that framework's idiomatic Lambda adapter. Ocel deploys `server.ts`
unmodified — that is the thing being tested — while sst and raw use
`handler.ts`. Both forms import the same routes so they cannot drift.

Platforms live in `platforms/<name>/driver.mjs` and export exactly two
functions; `runner/contract.mjs` states the shape. Adding a platform is one
file. The four ocel variants are one driver parameterised by an env overlay.

## One-time setup, by a human

Credentials go in `.env.local` (gitignored), read by the runner itself:

| name                    | what                                          |
| ----------------------- | --------------------------------------------- |
| `CLOUDFLARE_API_TOKEN`  | token for the account holding the zone         |
| `CLOUDFLARE_ACCOUNT_ID` | account the provider uploads workers to        |
| `OCEL_ACCESS_TOKEN`     | Ocel access token, in place of `ocel login`    |
| `OCEL_API_URL`          | Ocel API base URL                              |

AWS comes from the usual `AWS_*`/`AWS_PROFILE` environment.

Then, once per account:

```bash
ocel bootstrap --yes
```

Note the absent `--preview`. The e2e suites only ever stand up the preview
substrate; the ocel cells here deploy to **production**, which needs the
production deployments store. Previews are deliberately not used: they share one
global entry worker, so one cell would warm the worker the next cell measures a
cold start through.

The ocel cells also need the membrane Lambda layer to carry the entrypoint JS
you are benchmarking. Nothing in this repo builds or publishes that layer, and
`defaultMembraneLayerARN` in `platform/aws/provider/deploy/function.go` pins the
version. A change under `platform/aws/functions/entrypoints/` reaches no
function until a new version is published; point `OCEL_MEMBRANE_LAYER_ARN` at it
for a run, or move the default.

## Running it

```bash
pnpm bench                                        # the whole matrix
pnpm bench --frameworks express                   # one app, every platform
pnpm bench --frameworks hono --platforms ocel-bundle
pnpm bench --dry-run                              # the fake driver, no AWS
```

`--dry-run` exercises the orchestrator, measurement, stats and report against a
local server and spends nothing. Run it after changing the runner.

Expect roughly six minutes per cell. Interrupting with one `SIGINT` tears down
the in-flight cell and writes what was measured; a second abandons it, and then
the resources are yours to reclaim.

Results print as a table and land in `results/<timestamp>.json` with every raw
sample. That directory is gitignored: the numbers are specific to an account, a
region and the machine the probes ran from.

## Reading the table

`cold`/`warm` columns are client RTT measured from wherever you ran it, so they
carry your own network. `init` and `dur` come from the Lambda REPORT line and do
not. They are never blended, because the platforms do not share a network path —
ocel answers through a Cloudflare worker, sst and raw through a Lambda Function
URL. Comparing the RTT columns across platforms measures the architecture; the
REPORT columns measure the runtime.

Cold samples are forced one at a time by bumping a dummy environment variable on
the function, which guarantees a fresh execution environment. REPORT lines are
matched to phases by time window and classified cold by the presence of an
`Init Duration`, since a REPORT line cannot see our headers. When the number of
init-carrying lines does not equal the number of cold starts driven, the run
says so and that cell's cold figures should be treated as indicative.

## What it does not do

The pinned shape — runtime, memory, architecture, region — is asserted against
every deployed function, and a cell fails rather than quietly benchmarking a
different machine. Timeouts are pinned too. What is **not** equalised is the
network path above, and the raw baseline's Function URL grant is slightly
broader than SST's, because `add-permission` cannot express the
`InvokedViaFunctionUrl` condition SST attaches.

SST provisions an account-wide bootstrap (state and asset buckets, an ECR repo)
on its first deploy. Cell teardown leaves it alone deliberately, since all SST
cells share it; the first SST cell of a virgin account therefore carries the
bootstrap in its deploy time. `removeBootstrap({ confirm: true })` from
`platforms/sst/driver.mjs` clears it when you are done.
