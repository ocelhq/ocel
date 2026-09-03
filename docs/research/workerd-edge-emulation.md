# Emulating the Cloudflare edge with workerd/miniflare for PR journeys

Research for [#832](https://github.com/ocelhq/ocel/issues/832), part of map [#830](https://github.com/ocelhq/ocel/issues/830). Researched 2026-09-03.

Code facts are cited as `path:line` against `main` at `c818ed9a`. Cloudflare facts are cited to
developers.cloudflare.com, the `cloudflare/workers-sdk` and `cloudflare/workerd` repositories. Every
"observed" claim below was run on this machine against the versions the repo's own lockfile pins:
`miniflare@4.20260708.1` / `@cloudflare/workerd-linux-64@1.20260708.1` (via `wrangler@4.110.0`) and
`miniflare@4.20260310.0` / `workerd@1.20260310.1` (via `@cloudflare/vitest-pool-workers@0.12.4`).

## Short answer

- **Yes.** The production-built entry worker — the same `dist/index.js` byte-for-byte that
  `cli/node/generate.sh:40` embeds into the `ocel` binary — boots under bare `miniflare@4`, resolves a
  deployment over an RPC service binding, and proxies a SigV4-signed request to a plain local HTTP
  origin. Observed end to end; the probe is in §6.
- The origin is **data, not configuration**: `DeploymentRecord.functionUrls`
  (`platform/edge/cloudflare/workers/entry/src/deployments.ts:5-22`) is read out of the
  deployments-store at request time, so pointing the edge at a floci-emulated Lambda is a matter of
  writing a record, not of rebuilding or reconfiguring the worker.
- **One shape constraint, and it is cheap to satisfy.** `edgeOriginFetch` refuses to sign for any host
  that does not carry a `lambda-url.<region>` label pair
  (`platform/edge/cloudflare/workers/entry/src/signing.ts:2-8,28-30`). LocalStack's function-URL hosts
  (`<id>.lambda-url.<region>.localhost.localstack.cloud`) satisfy it as-is; a bare `127.0.0.1:PORT`
  origin does not, and returns 500. Observed both ways.
- **Everything the entry worker binds is locally emulated by miniflare**: R2, Durable Objects (SQLite,
  alarms), service bindings, plain-text/secret vars, the Cache API, and — the one that looked like the
  blocker — the **Worker Loader** binding, which the repo's own vitest suite already exercises
  (`platform/edge/cloudflare/workers/entry/vitest.config.mts:11`,
  `platform/edge/cloudflare/workers/entry/test/edge.test.ts`). There is **no KV binding anywhere** in
  the tree, so KV fidelity is a non-question.
- **What is not emulatable is the network, not the runtime**: zone worker routes, proxied DNS,
  Universal SSL, custom domains, `workers.dev` subdomains, tiered caching and colo behaviour. Ocel's
  edge provider spends most of `platform/edge/cloudflare/deploy/hostname.go` on exactly those, so a
  local edge cannot run the Go provider's attach path — it has to substitute for it.
- **The real gap is not Cloudflare, it is AWS.** The edge worker's cache entrypoint builds AWS
  endpoints from the region alone —
  `https://${bucket}.s3.${region}.amazonaws.com/` and `https://dynamodb.${region}.amazonaws.com/`
  (`platform/edge/cloudflare/workers/entry/src/cache-entrypoint.ts:42,133`) — with no endpoint
  override. A miniflare edge in front of floci reaches the origin fine, but the `use cache` /
  tag-clock path talks to the real AWS. Closing that needs an `OCEL_AWS_ENDPOINT`-shaped var.
- **Cloudflare as an origin target is already half-built.** The entry worker runs per-route app code
  through `env.LOADER` (`src/index.ts:359-365`, `src/edge.ts:54-122`), with per-bundle
  `compatibilityDate`/`compatibilityFlags` taken from the deployment record. That is workers-as-compute
  in everything but name, and it already runs under workerd locally. A `cloudflare` origin target would
  reuse the same emulator, not a new one.
- **Cost is negligible**: the 646 existing worker tests run in 6.5 s wall on this machine, and the
  bare-miniflare probe boots two workers and answers in about a second. workerd ships as a prebuilt
  Linux binary in the lockfile; CI needs no Docker and no Cloudflare account.

## 1. What the edge is today

### 1.1 Three workers, built by wrangler, embedded in the Go binary

| Worker | Package | Bundle | Role |
|---|---|---|---|
| entry | `@platform/cf-entry` | 188 KB, one ESM file | serves every request |
| deployments-store | `@platform/cf-deployments-store` | 17 KB | DO-SQLite ledger of deployment records |
| isr-writer | `@platform/cf-isr-writer` | 17 KB | DO-SQLite + R2 ISR/tag writer |

All three build with `wrangler deploy --dry-run --outdir=dist`
(`platform/edge/cloudflare/workers/{entry,isr-writer,deployments-store}/package.json:7`) — wrangler's
own esbuild, everything inlined, no external modules. `cli/node/generate.sh:36-42` runs the three
builds and copies each `dist/index.js` into `cli/node/dist/workers/`, which `cli/node/node.go:15`
`go:embed`s. At deploy time the CLI unpacks that tree and hands the provider three manifests of
absolute paths (`cli/node/node.go:30-46`), read back through
`OCEL_WORKER_BUNDLES` / `OCEL_STORE_WORKER_BUNDLES` / `OCEL_ISR_WRITER_WORKER_BUNDLES`
(`platform/edge/contract/bundles.go:9,16-17`).

**The artefact a local runtime would load is therefore already produced, already single-file, and
already the exact bytes production runs.** No separate "test build" is needed.

### 1.2 wrangler.jsonc is dev/test only; production metadata is hand-built in Go

Each worker has a `wrangler.jsonc`, but real deploys never use it: `deploy/cloudflare.go:581-616`
marshals `{main_module, compatibility_date, compatibility_flags, observability, bindings}` into
multipart script-upload metadata and PUTs it through `cloudflare-go`. The compat settings are
duplicated as Go constants — `const compatDate = "2026-07-13"` and
`var compatFlags = []string{"nodejs_compat"}` (`deploy/cloudflare.go:37-39`) — matching each
`wrangler.jsonc`'s `compatibility_date` / `compatibility_flags`.

The two are kept in step by hand. `cli/node/generate.sh:14-15` hashes `wrangler.jsonc` into the
embed stamp, so a config edit does rebuild the bundle, but nothing checks that the Go constants agree
with it. (Observability already differs: `{enabled:true}` in wrangler.jsonc versus the richer
head-sampling/logs/traces block at `deploy/cloudflare.go:41-53`.)

### 1.3 Bindings

The complete set of binding types this codebase can emit — `deploy/cloudflare.go:633-676` plus
`deploy/stack.go:386-400`: `assets`, `r2_bucket`, `worker_loader`, `service`, `plain_text`,
`secret_text`, `durable_object_namespace`, `inherit`. **No KV.**

Entry worker (`platform/edge/cloudflare/workers/entry/src/env.ts:11-31`):

| Binding | Type | Emulated by miniflare? |
|---|---|---|
| `ASSETS` | Workers Static Assets, `run_worker_first:true` (`cloudflare.go:591-594`) | assets yes; `run_worker_first` needs checking per version |
| `OCEL_CACHE_STORE` | R2 bucket | yes |
| `LOADER` | **`worker_loader`** (`cloudflare.go:626-631`) | yes — `workerLoaders` |
| `DEPLOYMENTS` | service binding → `ocel-deployments-store`, called by **RPC** `pointerRecord()` | yes |
| `ISR_WRITER` | service binding → `ocel-isr-writer` | yes |
| `OCEL_SLUG`, `OCEL_APP`, `OCEL_PREVIEW*`, `OCEL_AWS_REGION`, `OCEL_STATE_TABLE`, `OCEL_ISR_BUCKET`, `OCEL_IMAGE_OPTIMIZER_URL`, `OCEL_REVALIDATE_QUEUE_URL`, `OCEL_ORIGIN_BODY_*`, `OCEL_EDGE_ACCESS_KEY_ID` | plain_text | yes |
| `OCEL_EDGE_SECRET_KEY` | secret_text | yes |

deployments-store: `DEPLOYMENTS_DO` (DO SQLite, class `DeploymentsStore`) + `BOOTSTRAP_SECRET`.
isr-writer: `ISR_WRITER_DO` (`IsrDeploy`), `ISR_SNAPSHOT_DO` (`IsrSnapshot`), `OCEL_CACHE_STORE` (R2),
`BOOTSTRAP_SECRET`. Migrations are computed as a delta against the deployed script
(`deploy/stack.go:436-462`), which a local run does not need — miniflare applies the
`new_sqlite_classes` log from `wrangler.jsonc` directly.

### 1.4 How a request finds its origin

`platform/edge/cloudflare/workers/entry/src/index.ts:287-371`:

1. `host` comes from `request.url`.
2. Preview modes decode `slug--pointer[--app]` (global) or `pointer--app` (per-project) out of the
   host label (`src/preview.ts`).
3. `resolveServe` → `src/deployments.ts:74-118`: an in-worker LRU keyed by host, 5 s TTL, 64 entries,
   then the RPC `env.DEPLOYMENTS.pointerRecord({slug, app, pointer, knownIdentity})`.
4. deployments-store hops to its DO (`workers/deployments-store/src/index.ts:116-127`) and reads
   SQLite.
5. `runtimeFor` (`src/index.ts:157-159`) picks `routedRuntime` when the record carries a
   `routingManifest` (full `@framework/next-router`: cache, prerender, edge invoker) and
   `originRuntime` otherwise — `nodeOrigin`, a straight proxy to the single function URL
   (`src/node.ts:18-71`).

**Nothing about the origin is baked into the worker.** `functionUrls`, `assetPrefix`, `isrPrefix`,
`isrWriteSecret`, `edgeWorkers` and the sealed env all arrive in the record.

### 1.5 What the Go provider does that has no local equivalent

`deploy/hostname.go` is the part that cannot be emulated because it is Cloudflare's *network*, not its
runtime: custom domains are actively detached (`hostname.go:31-33,247-264`), zone worker routes are
created as `hostname + "/*"` (`:132-134`) after a longest-suffix zone match (`:339-353`), the DNS
record must exist and be proxied or the deploy refuses (`:199-215`), and Universal SSL coverage is
warned about for multi-label subdomains (`:51-53,102-111`). `workers.dev` subdomains are enabled only
when an app has no domains (`cloudflare.go:466`).

A local edge substitutes for all of that with "one miniflare instance, dispatch with a `Host` header".

## 2. What the existing tests already cover

All three workers use `@cloudflare/vitest-pool-workers` (declared `^0.12.4`, resolved 0.12.21, which
pins `miniflare@4.20260310.0` exactly) with `defineWorkersConfig` and
`wrangler: { configPath: "./wrangler.jsonc" }`. Entry adds a miniflare block
(`workers/entry/vitest.config.mts:5-16`): `isolatedStorage: false`, a `DEPLOYMENTS` service binding
stubbed as a function returning 501, `workerLoaders: { LOADER: {} }`, and
`r2Buckets: ["TAG_SNAPSHOT_STORE"]`.

- 34 test files, **646 tests, 24 files, 6.45 s** for `@platform/cf-entry` alone (observed).
- `cloudflare:test` surface in use: `env`, `SELF`, `createExecutionContext`, `runInDurableObject`,
  `runDurableObjectAlarm`.
- `entry/test/edge.test.ts` builds synthetic edge bundles and drives them through the real
  `createEdgeInvoker` — **the Worker Loader path is already covered under workerd today**.
- `deployments-store/test/store.test.ts` and `isr-writer/test/{registry,build,index}.test.ts` cover DO
  SQLite and alarms.

What they do **not** cover: the built bundle (they test `src/`), a real origin over the network (every
origin is a stub), host-based routing beyond the preview decoders, and anything multi-request across
a deployment change.

**CI gap.** `.github/workflows/build.yml:76` runs only `pnpm --filter @platform/cf-entry test`. The
isr-writer, deployments-store and `@platform/cf-auth` suites — 10 files, all the Durable Object and
alarm coverage — **are never run in CI**, despite having complete pool configs. Filed as
[#845](https://github.com/ocelhq/ocel/issues/845).

## 3. miniflare 4 / workerd: what is emulated

"Miniflare is a simulator for developing and testing Cloudflare Workers. It's written in TypeScript,
and runs your code in a sandbox implementing Workers' runtime APIs" —
<https://developers.cloudflare.com/workers/testing/miniflare/>. The sandbox is workerd itself: the
`miniflare` package depends on the `workerd` npm package, which ships a prebuilt native binary per
platform (`@cloudflare/workerd-linux-64` is in this repo's lockfile). `wrangler dev` and
`@cloudflare/vitest-pool-workers` are both wrappers over the same Miniflare API, which is why the
repo's existing test config can reach through to raw `miniflare` options
(`workers/entry/vitest.config.mts:9-15`).

"All resources your Worker is bound to in your Wrangler configuration are simulated locally … your
Worker code interacts with these bindings using the exact same API calls (such as `env.MY_KV.put()`)
as it would in a deployed environment" —
<https://developers.cloudflare.com/workers/development-testing/>.

The shipped `miniflare@4.20260708.1` type definitions enumerate the simulators, which is the most
precise available list: `kvNamespaces`, `r2Buckets`, `d1Databases`, `durableObjects`,
`serviceBindings`, `queueProducers`, `queueConsumers`, `workflows`, `workerLoaders`,
`analyticsEngineDatasets`, `hyperdrives`, `ratelimits`, `secretsStoreSecrets`, `dispatchNamespaces`,
`vectorize`, `browserRendering`, `images`, `email`, `pipelines`, `mtlsCertificates`, `tails`,
`wrappedBindings`, plus `outboundService`, `fetchMock`, `unsafeDirectSockets` and
`remoteProxyConnectionString`.

**Everything the ocel edge binds is in the locally-simulated set.** Point by point:

| Binding | Verdict | Evidence |
|---|---|---|
| R2 (`OCEL_CACHE_STORE`, `TAG_SNAPSHOT_STORE`) | full local | `r2Buckets`; already used by the repo's tests |
| Durable Objects, SQLite, alarms | full local, **never remote** | `durableObjects`; "Durable Objects … cannot use `remote: true`" — development-testing page. Exercised by `runInDurableObject` / `runDurableObjectAlarm` in the repo's suites |
| service bindings, incl. RPC `WorkerEntrypoint` | full local | observed in §6: `pointerRecord()` RPC across two miniflare workers |
| plain_text / secret_text vars | full local, **never remote** | "Environment Variables (vars) … Secrets" are in the unsupported-remote list |
| Static Assets + `run_worker_first` | full local, **never remote** | observed in §6.3; "Static Assets" is in the unsupported-remote list |
| **Worker Loader** (`LOADER`) | full local | "The Worker Loader API is available in local development with Wrangler and workerd. But to run dynamic Workers on Cloudflare, you must sign up for the closed beta." — <https://developers.cloudflare.com/workers/runtime-apis/bindings/worker-loader/>. Observed in §6.4 |
| Cache API (`caches.default`) | local, persistable | observed in §6.5 |
| `request.cf` | populated, but fetched from the network by default | observed in §6.6 |
| KV | full local — and **irrelevant**, ocel binds none | — |

The Worker Loader status is worth reading twice: **local development is the *better*-supported side**.
Dynamic Workers are still closed beta on the production network, and ocel's per-route edge functions
depend on them. A workerd emulator is the only place that path can be exercised without an allow-listed
account.

Not locally simulated at all, per the same page — "There is no current local simulation for" Browser
Run, Workers AI, Vectorize, or mTLS bindings, and Images is "limited with only a subset of features".
Ocel binds none of these.

## 4. What is not emulated

The gaps split cleanly: **workerd reproduces the runtime; nothing reproduces Cloudflare's network or
its enforcement.**

### 4.1 No network, so no routing

There is no zone, no route table, no proxied DNS record, no Universal SSL, no custom domain and no
`workers.dev` subdomain. Miniflare serves one HTTP listener and dispatches whatever arrives to the
worker; `request.url` and the `Host` header are whatever the caller sends. Observed: dispatching with
`host: app.example.com` reaches the worker with that host intact, which is all the entry worker's
preview decoders and deployment lookup need (`src/index.ts:295-320`).

This is not a fidelity loss for the *worker*; it is a fidelity loss for `deploy/hostname.go`. A local
edge cannot test "did we attach the route to the right zone", "is the record orange-clouded"
(`hostname.go:199-215`), or the Universal SSL multi-label warning (`hostname.go:51-53`). Those stay
dispatch-only against a real account.

### 4.2 No limits are enforced

Production caps subrequests at "50/request" on free and "10,000/request (up to 10M)" on paid, and CPU
time at "10 ms" free / "5 min (default: 30 seconds)" paid —
<https://developers.cloudflare.com/workers/platform/limits/>. Observed locally: **120 sequential
subrequests all completed**, and a **5-second busy loop returned 200**. A journey that passes locally
can therefore still exceed limits in production. The entry worker's fan-out is bounded by the routing
manifest rather than by request shape, so this is a low-probability trap — but it is a real one, and
it means the emulator can never certify "this deployment fits in the budget".

### 4.3 Cache API divergences

`caches.default` is real and persistable locally (§6.5), but the documented production semantics have
no local analogue: "the contents of the cache do not replicate outside of the originating data
center", "`cache.put` is not compatible with tiered caching", and `stale-while-revalidate` /
`stale-if-error` "are not supported" with `cache.put()`/`cache.match()` —
<https://developers.cloudflare.com/workers/runtime-apis/cache/>. So a local run has exactly one
"colo" and a cache that never evicts under memory pressure. Tests can assert *that* an entry was
written and *that* a second request hits; they cannot assert edge coherence timing, which is the
interesting part of the ISR story.

### 4.4 `request.cf` is real, and that is the problem

Miniflare populates `request.cf` by fetching it "from a trusted Cloudflare endpoint" — the reference
page describes the `cf` option as managing exactly that, with caching and a custom-file override.
Observed on this machine, an unconfigured instance returned live geo: `colo: "JNB"`, `country: "KE"`,
`asOrganization: "Safaricom Limited"`. In CI that is a **network call on startup and a
non-deterministic value**. Any harness must pass `cf: false` or a fixed object.

### 4.5 workerd version drift

`compatibility_date` is `2026-07-13` (`deploy/cloudflare.go:37`, and each `wrangler.jsonc`). The
vitest pool pins `miniflare@4.20260310.0` / `workerd@1.20260310.1` — a runtime built four months
*before* that date. Observed: **workerd does not reject a future compatibility date**; the entry
suite's 646 tests pass on it. The practical consequence is subtler than a hard failure: a flag or
default that landed between the workerd build and the compat date is silently absent locally. Pinning
the emulator's workerd forward of the compat date is a cheap invariant for the harness to assert.

### 4.6 A named-export quirk that bites the built bundle specifically

Under bare miniflare, workerd validates **every** named export of the entrypoint module as a candidate
entrypoint. The built entry bundle exports `EDGE_HEADER`, `serve`, `resolveServe`, `dispatchResult`,
`resolveRouteDeps` and `withEdgeHeader` alongside `default` and `CacheEntrypoint`. Booting it directly
fails:

```
service core:user:entry: Uncaught TypeError: Incorrect type for map entry 'EDGE_HEADER':
the provided value is not of type 'function or ExportedHandler'.
```

Reproduced with a two-line worker (`export const X = "s"; export default {fetch}` fails; without the
string export it starts). The vitest pool does not hit this because it wraps the module rather than
making it the top-level entrypoint. The fix is one wrapper module re-exporting only `default` and
`CacheEntrypoint` (plus `modulesRules: [{ type: "ESModule", include: ["**/*.js"] }]`, since miniflare
treats `.js` as CommonJS by default). Worth knowing before someone concludes the bundle "doesn't run
locally".

### 4.7 The AWS side, which is the actual blocker

The entry worker's `CacheEntrypoint` — the RPC surface handed to loaded edge workers for Next's
`use cache` — builds AWS endpoints from the region alone:

```ts
`https://${deps.fetchBucket}.s3.${deps.region}.amazonaws.com/`   // cache-entrypoint.ts:42
`https://dynamodb.${deps.region}.amazonaws.com/`                 // cache-entrypoint.ts:133
```

There is no endpoint override. A miniflare edge in front of a floci origin will reach the origin
correctly and then send its cache and tag-clock traffic to **real AWS**. `OCEL_REVALIDATE_QUEUE_URL`
is a full URL and therefore already redirectable (`src/signing.ts`, `sqsRegion`), and assets/ISR reads
go through the R2 binding — so S3 and DynamoDB are the only two, and closing them is a small,
contained change (an `OCEL_AWS_ENDPOINT`-shaped var threaded into `awsServiceFetch`).

## 5. Cloudflare as an origin target

**The compute path already exists and already runs locally.** `createEdgeInvoker`
(`src/edge.ts:54-122`) reads an edge bundle out of R2, builds a `WorkerLoaderWorkerCode` with
`mainModule`, `modules`, and — critically — a `compatibilityDate`/`compatibilityFlags` pair *taken from
the deployment record, not from the host worker* (`src/edge.ts:16-19`), then calls
`loader.get(id, load).getEntrypoint(undefined, { props: { entryKey } }).fetch(request)`. That matches
the documented `WorkerCode` shape exactly: "`mainModule`: The name of the Worker's main module",
"`compatibilityDate`: Required", "`env`: The environment object providing custom bindings to the
dynamic Worker", "`globalOutbound`: Controls network access" —
<https://developers.cloudflare.com/workers/runtime-apis/bindings/worker-loader/>.

Observed (§6.4): a bare miniflare instance loads a dynamic worker through `env.LOADER`, hands it its
own `env`, and runs `node:async_hooks` `AsyncLocalStorage` inside it under `nodejs_compat`. So
workers-as-compute is emulatable today, at the same fidelity as the edge worker itself.

What a `cloudflare` origin target would still need to decide, none of which the emulator answers:

- **Where app code lives.** Today an edge bundle is an R2 object keyed by `bundleKey`, sealed
  variables alongside it (`src/edge.ts:33-41,128-150`). A first-class origin target might instead be
  an ordinary Worker script per app, which is a provisioning question, not a runtime one.
- **State.** The AWS origin's Postgres/blob resources have no Cloudflare equivalent bound today; a
  `cloudflare` target has to answer that before a journey means anything.
- **The Dynamic Workers beta.** Production needs an allow-listed account. Local does not. Journeys can
  run ahead of the beta; dispatch runs cannot.
- **`edgeconformance`.** `platform/edge/contract/edgeconformance/conformance.go` is the suite any new
  edge kind must satisfy — including `PutStaged`/`Promote` against a ledger. A local edge kind would
  be the second implementation and would flush out contract assumptions that are currently
  Cloudflare-shaped.

## 6. The probe

Everything below was run on this machine on 2026-09-03 against `miniflare@4.20260708.1` /
`workerd@1.20260708.1`, using the real `platform/edge/cloudflare/workers/entry/dist/index.js` produced
by `pnpm --filter @platform/cf-entry build`.

### 6.1 The built entry worker serves a local origin

```js
const record = {
  app: "web", framework: "node", identity: "dep-1", deploymentId: "dep-1", entry: "server",
  functionUrls: { server: originUrl },
  assetPrefix: "assets/", isrPrefix: "isr/", createdAt: Date.now(),
};

const mf = new Miniflare({
  workers: [
    {
      name: "entry",
      modulesRoot: "/",
      scriptPath: ".../workers/entry/dist/wrap.js",   // re-exports default + CacheEntrypoint, see §4.6
      modules: true,
      modulesRules: [{ type: "ESModule", include: ["**/*.js"] }],
      compatibilityDate: "2026-07-13",
      compatibilityFlags: ["nodejs_compat"],
      bindings: {
        OCEL_APP: "web", OCEL_SLUG: "dev",
        OCEL_EDGE_ACCESS_KEY_ID: "test", OCEL_EDGE_SECRET_KEY: "test", OCEL_AWS_REGION: "us-east-1",
      },
      serviceBindings: { DEPLOYMENTS: { name: "deployments" } },
      workerLoaders: { LOADER: {} },
      r2Buckets: { OCEL_CACHE_STORE: "cache" },
    },
    {
      name: "deployments",
      modules: true,
      compatibilityDate: "2026-07-13",
      compatibilityFlags: ["nodejs_compat"],
      bindings: { RECORD: JSON.stringify(record) },
      script: `
        import { WorkerEntrypoint } from "cloudflare:workers";
        export default class extends WorkerEntrypoint {
          async pointerRecord({ knownIdentity }) {
            const record = JSON.parse(this.env.RECORD);
            if (knownIdentity === record.identity) return { kind: "unchanged", identity: record.identity };
            return { kind: "record", identity: record.identity, record };
          }
        }
      `,
    },
  ],
});

await mf.dispatchFetch("http://app.example.com/some/path", { headers: { host: "app.example.com" } });
```

Results, against a `node:http` server on an ephemeral port:

| `functionUrls.server` | Result |
|---|---|
| `http://127.0.0.1:PORT` | **500** — `cannot sign request to non-Function-URL host: 127.0.0.1:PORT` |
| `http://abc.lambda-url.us-east-1.localhost.localstack.cloud:PORT` | **200**, `x-ocel-edge: cloudflare`, origin saw `GET /some/path` with `authorization: AWS4-HMAC-SHA256 Credential=…` |

The second row is the finding. The real bundle, the real deployment record, the real RPC hop, a real
SigV4 signature, and a plain local HTTP origin. `localhost.localstack.cloud` and its wildcards resolve
publicly to loopback, so a floci function URL satisfies `lambdaRegion` (`src/signing.ts:2-8`) without
any host trickery.

Two caveats for a harness built on this:

- **floci publishes LocalStack's 4566 on a random host port** (`scripts/floci.sh:74`,
  `-p 127.0.0.1::4566`), so the function URL LocalStack reports carries `:4566` while the reachable
  port is the mapped one. Either publish 4566 fixed for journey runs, or rewrite the port into the
  record.
- Both requests hit the origin: the unrouted `nodeOrigin` path (`src/node.ts:18-71`) is a pure proxy
  and never consults `caches.default`. Edge caching only exists on the routed (Next) path, so a
  cache-behaviour journey needs a `routingManifest` in the record.

### 6.2 The existing suite, timed

`pnpm --filter @platform/cf-entry test` — **24 files, 646 tests, 6.45 s** wall on workerd
1.20260310.1. Startup dominates; the assertions take 4.6 s.

### 6.3 Static Assets with `run_worker_first`

`assets: { directory, binding: "ASSETS", routerConfig: { has_user_worker: true,
invoke_user_worker_ahead_of_assets: true } }` (and a `name` on the worker) reproduces the production
shape ocel deploys (`deploy/cloudflare.go:591-594`): `/hello.txt` → 200 from disk, `/dynamic` → 200
from the worker, `/missing` → 404.

### 6.4 Worker Loader

`workerLoaders: { LOADER: {} }` on a bare instance; the host worker calls
`env.LOADER.get("dyn-1", async () => ({ compatibilityDate, compatibilityFlags: ["nodejs_compat"],
mainModule: "main.js", modules: {...}, env: { GREETING: "hi" } })).getEntrypoint().fetch(request)`.
Returned 200 with the loaded worker's own `env` and a working `AsyncLocalStorage`.

### 6.5 Cache API

A worker that `caches.default.put`s then `match`es returned `MISS`/`HIT` across two `dispatchFetch`
calls, both in-memory and with `cachePersist` pointed at a directory.

### 6.6 `request.cf`, limits

`request.cf` came back fully populated with **live** geo (`colo: "JNB"`, `country: "KE"`,
`asOrganization: "Safaricom Limited"`, `clientTcpRtt: 55`). 120 sequential subrequests all succeeded;
a 5-second CPU busy loop returned 200 in 5002 ms.

## 7. Gaps, in the order they would bite

1. **S3/DynamoDB endpoints are hardcoded** (`src/cache-entrypoint.ts:42,133`). Until an endpoint
   override exists, a journey that exercises Next `use cache` or the tag clock either talks to real
   AWS or must be scoped out. This is the one code change the emulation actually requires.
2. **floci's port mapping vs LocalStack's advertised function-URL port** (`scripts/floci.sh:74`).
   Mechanical, but it decides whether records can be used as LocalStack reports them.
3. **`cf` must be pinned** (`cf: false` or a fixed object) or every CI run makes a network call and
   sees different geo.
4. **The bundle needs a wrapper module** to boot under bare miniflare (§4.6), plus `modulesRules` for
   ESM. Two lines, but non-obvious.
5. **Who writes the deployment record.** The Go client posts to the deployments-store over plain HTTP
   with a bearer token and no scheme enforcement (`deploy/store.go:172-186`), so a locally-hosted
   deployments-store worker can be seeded by the *real* code path. That is the honest option; a
   hand-written stub (as in §6.1) is the fast one and skips the store's own logic.
6. **What substitutes for `deploy/hostname.go`.** Routes, DNS, SSL and custom domains have no local
   analogue. Either the harness drives the worker directly and the Go attach path stays dispatch-only,
   or a new `edge.Kind` satisfying `edgeconformance` is written for local runs — a much larger
   commitment.
7. **Limits and cache coherence are unassertable locally** (§4.2, §4.3). Journeys should not claim to
   cover them.
8. **workerd/compat-date drift** (§4.5). Cheap to assert, easy to forget.
9. **Three worker suites never run in CI** — [#845](https://github.com/ocelhq/ocel/issues/845).

One reframing worth carrying back to the map: `edges.DefaultKind` is **CloudFront**, not Cloudflare
(`platform/aws/provider/edges/registry.go:32`), and `livefloci_test.go` bootstraps `DefaultKind`. Every
example nonetheless opts into `edge: cloudflare()` in its config. So "what stands in for Cloudflare"
is a question about the example configs as much as about the emulator — a PR journey could equally
answer it by not selecting the Cloudflare edge.
