# Plain node app deployment e2e

Deploys a small express app for real (`ocel build` + `ocel preview up --prebuilt`,
same two-step drive as `scripts/e2e-next`'s) and proves the V8 compile-cache
legs `cloud/aws/deploy/bytecode.go`/`warm.go` build now reach **any** `nodejs*`
function, not just a Next one. Unlike `scripts/e2e-next`, nothing external
drives this — there is no adapter compatibility harness for a plain node app to
plug into — so `deploy.mjs` builds its own temp app identity and drives the
whole deploy itself, and `assert-bytecode.mjs`/`assert-embed.mjs` are run by
hand against the URL it prints.

| script                | what it does                                                            |
| ---------------------- | ------------------------------------------------------------------------ |
| `deploy.mjs`            | stages `smoke-app/` into a fresh temp dir, builds and deploys it, prints **only** the URL to stdout |
| `cleanup.mjs`           | tears the deployment down, synchronously, from that temp dir            |
| `assert-bytecode.mjs`   | proves the warm-then-S3-rehydrate legs, run by hand against the URL      |
| `assert-embed.mjs`      | proves the embedded-artifact leg in its place, run by hand               |

`lib.mjs` holds this harness's own pure logic (unit tested: `pnpm --filter
@ocel-scripts/e2e-node test`) plus everything framework-agnostic re-exported
from `@ocel-scripts/e2e-shared/lib.mjs` — the same module `scripts/e2e-next`
now imports from, so the two harnesses read one implementation of project/slug
derivation, the bytecode-cache key shape and its CloudWatch line matching,
rather than two that could drift. `aws.mjs` is the identical re-export of
`@ocel-scripts/e2e-shared/aws.mjs`. `sigv4.mjs` is the one piece of AWS
plumbing this harness needs that `scripts/e2e-next` does not — see "Why every
request here is signed" below. `guard-accounts.sh` refuses to deploy anywhere
but the disposable account; it execs `scripts/e2e-shared/guard-accounts.sh`,
the same check `scripts/e2e-next` runs.

## What the smoke app is, and why it stays this small

`smoke-app/` is a plain express app: one route (`/health`) the harness polls
for readiness, one route (`/echo`) it bursts against to force fresh sandboxes
and that proves the app is serving its own correct response rather than a
cached or default one, and nothing else — no queues, no crons, no other
`ocel` resource. That matters for one concrete reason: the deploy's warm pass
fans out against the account's own Lambda concurrency quota
(`warmConcurrency = appConcurrency`, `cloud/aws/deploy/warm.go`), which on the
disposable e2e account is **10**. This app realizes exactly one Lambda
function, so that limit is never in play *for the deploy's own warm pass*; an
app that grew a second or third function here would be throttling its own
warm pass long before it proved anything.

The same quota is not out of play everywhere, though: the assertions' own
read-leg burst (`REHYDRATE_BURST_SIZE`/`BURST_SIZE`, both 20, in
`assert-bytecode.mjs` and `assert-embed.mjs`) is sized to force fresh
sandboxes, not to stay under the account's concurrency quota, and 20 exceeds
it. Some of those requests can come back throttled (HTTP 429) — `burstEcho`
counts and logs that rather than folding it into "failed", and the assertion
only requires enough requests to have gotten through, not all of them.

`ocel`/`@ocel/provider-aws` are **not** npm dependencies of `smoke-app/` — they
are symlinked in from the sidecar by `linkSidecar`
(`@ocel-scripts/e2e-shared/sidecar.mjs`), the same way `scripts/e2e-next`'s
temp apps get them. Only `express` is a real dependency, installed from the
registry into the temp directory `deploy.mjs` creates — see "Repacking the
sidecar" in `scripts/e2e-next/README.md` for how the sidecar itself is built;
this harness reuses whatever `$OCEL_E2E_SIDECAR_DIR` already points at.

## What `assert-bytecode.mjs` and `assert-embed.mjs` prove

Exactly what `scripts/e2e-next`'s two scripts of the same name prove — see
that package's README for the full, unabridged reasoning, which this harness's
copies do not restate: the object is whole in S3 before `assert-bytecode.mjs`'s
first request, the deploy's own warm summary attributes it to the warm pass
and covers the whole bundle, and (`assert-embed.mjs`) a burst of concurrent
requests forces fresh sandboxes and every one of them loads the cache from its
own artifact rather than falling through to S3.

Embedding is no longer its own gate (`cloud/aws/deploy/embed.go`): whenever
`OCEL_BYTECODE_CACHE=1` turns the feature on at all — which `deploy.mjs`
always does — the deploy also runs the embed pass, unconditionally, on every
eligible function. One deploy is therefore enough for both scripts: cold
starts never fetch the S3 object at all, so `assert-bytecode.mjs`'s own
read-leg burst is unconditionally skipped (loudly, with a warning naming
`assert-embed.mjs` as the script that actually proves the read leg) in favor
of a single request standing in for its correctness check, and
`assert-embed.mjs` needs no flag to decide whether it applies.

Three things are genuinely different for a plain node app, and are each
called out at the top of the relevant script rather than only here:

- **No entry table.** `loadUserApp`
  (`packages/lambda-entrypoints/src/node/entrypoint.mts`) imports the app's
  whole module graph at `INIT`, before a warm invocation can ever reach the
  handler — there is nothing left to warm by the time one arrives. The
  report-only warm handler this branch added there answers `entries:1,
  loaded:1, stoppedBy:"complete"` for that one unit instead of walking
  anything, and `assert-bytecode.mjs` additionally fails if the deploy's warm
  summary instead carries `"node did not report back on the compile-cache
  warm"` — the line `cloud/aws/cmd/lambdanode/bootstrap/warm.go`'s
  `warmSummary.count` would have produced before that handler existed, which
  would mean the object landed with nobody having measured what went into it.
- **No caching tier in front of the app.** No framework this deploy pipeline
  knows registers a Cloudflare worker except `next`
  (`cloud/edge/framework/registry.go`), so a plain node app is served straight
  from its own Lambda Function URL. `scripts/e2e-next`'s burst needs a
  force-dynamic route (`TAG_PROBE_ROUTE`) to guarantee it reaches the Lambda
  past Next's own response cache; this harness needs no such trick — `/echo`
  already reaches the Lambda on every request.
- **Correctness is folded into the burst, where there is one.**
  `assert-embed.mjs`'s burst response is parsed and checked against
  `ECHO_MARKER` — the same requests that force fresh sandboxes and prove the
  read leg also prove the app answers correctly with the cache in place.
  `assert-bytecode.mjs` has no burst to fold it into (its read leg is always
  skipped — see above), so it makes a single signed request instead.

### Why every request here is signed

Every Lambda Function URL this deploy pipeline provisions carries
`AuthorizationType: AWS_IAM` (`cloud/aws/deploy/function.go`) — that is true
of **every** function, not only ones behind a worker. A Next app's requests
still reach `fetch()` unsigned in `scripts/e2e-next`'s assertions because they
land on a Cloudflare worker that signs its own forward
(`workers/nextjs/src/signing.ts`); a plain node app registers no such worker,
so nothing signs on this harness's behalf. `sigv4.mjs` signs every request
this harness sends — health checks, the correctness/burst requests, all of
it — with `aws4fetch` (already a dependency elsewhere in this repo for the
identical purpose, at the edge rather than from a harness), under whatever
`AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`/`AWS_SESSION_TOKEN` are already in
the environment for the `aws` CLI calls this package's `aws.mjs` makes.

## Running it

There is no CI workflow for this harness yet — everything below is run by
hand, against the same disposable AWS/Cloudflare accounts and sidecar
`scripts/e2e-next/README.md`'s "One-time setup" section provisions. Nothing
here needs a Cloudflare wildcard DNS record: a plain node app registers no
worker to route through it.

```bash
export ADAPTER_DIR=<path to a checkout with a built cloud/aws provider>
export OCEL_E2E_SIDECAR_DIR=<the sidecar directory>
export AWS_ACCESS_KEY_ID=... AWS_SECRET_ACCESS_KEY=... # + AWS_SESSION_TOKEN if applicable
export EXPECTED_AWS_ACCOUNT_ID=... EXPECTED_CLOUDFLARE_ACCOUNT_ID=...
export CLOUDFLARE_API_TOKEN=... CLOUDFLARE_ACCOUNT_ID=...

scripts/e2e-node/guard-accounts.sh

url=$(node scripts/e2e-node/deploy.mjs)
echo "deployed: $url"

# From the temp app directory deploy.mjs printed to stderr ("staged smoke app
# in <dir>") — assert-*.mjs read .ocel/deploy-result.json from there. One
# deploy is enough for both: embedding is unconditional whenever bytecode
# caching is on, so nothing here needs a second flag or a second deploy.
cd <that temp dir>
node "$OLDPWD/scripts/e2e-node/assert-bytecode.mjs" "$url"
node "$OLDPWD/scripts/e2e-node/assert-embed.mjs" "$url"

node "$OLDPWD/scripts/e2e-node/cleanup.mjs"
```

`deploy.mjs` sets `OCEL_BYTECODE_CACHE=1` on the child `ocel` process itself —
the feature is off by default (`cloud/aws/deploy/bytecode.go`), and this
harness's whole point is proving the legs it turns on.

## Known limits (accepted, not bugs)

- No CI workflow drives this yet — it is a manual proof, not a gate. Wiring it
  into `.github/workflows/test-e2e-deploy.yml` (or a sibling) is future work.
- The lambdanode membrane layer this deploy warms against is whatever
  `$ADAPTER_DIR`'s provider was built to pin — same caveat as
  `scripts/e2e-next`'s "membrane layer is pinned" limit.
- A cancelled run strands its project the same way `scripts/e2e-next`'s does:
  `cleanup.mjs` is the only footprint control, and it cannot run if the
  process is killed. Reclaim one with `ocel preview rm --name <slug> --yes`
  from a directory whose `ocel.config.ts` declares that slug —
  `deploy.mjs`'s stderr line ("project `<slug>` in `<dir>`") is where to read
  it back.
