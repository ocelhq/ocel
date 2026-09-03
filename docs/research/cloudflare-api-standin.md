# Standing in for the Cloudflare API on a PR runner

Research for [#860](https://github.com/ocelhq/ocel/issues/860), part of the test-suite map [#830](https://github.com/ocelhq/ocel/issues/830).

**Verdict: nothing stands in. Stay red at `up` on floci, green on dispatch only.**

Cloudflare ships no emulator of its management API, and the closest thing — the prism mock
`cloudflare-go` runs its own tests against — rejects ocel's script upload, 401s its
credential check, crashes on `content/v2`, and is stateless besides. Everything in
workers-sdk is a client or a runtime. The real-account route needs a paid plan for a binding
ocel puts on every worker, secrets a fork PR cannot be given, and a tunnel Cloudflare
documents as testing-only.

Two premises in the ticket are corrected below: the base URL is *already* overridable
without `NewAt`, and overriding it is not enough, because two of ocel's wires are hardcoded
hostnames.

## Method

- **Code** — `platform/edge/cloudflare/deploy/*.go` at `78b33b66`, and the pinned
  `cloudflare-go/v4 v4.6.0` module on disk. Cited by file and line.
- **Docs** — Cloudflare's own developer docs, GitHub's Actions docs, and the workers-sdk and
  api-schemas repositories. Cited inline; read on 2026-09-03.
- **Live** — a prism mock server run on this machine on 2026-09-03 against Cloudflare's
  published OpenAPI spec, driven by a Go program using `cloudflare-go/v4` the way `deploy/`
  uses it. Every line marked **Live** was observed, not inferred.

## What ocel actually calls

Read off `platform/edge/cloudflare/deploy/*.go` at `78b33b66`. Paths are the ones
`cloudflare-go/v4 v4.6.0` builds, confirmed against the SDK source and observed on the wire
(see "The prism experiment").

| # | Call | Path | Reached from |
| --- | --- | --- | --- |
| 1 | `Accounts.Get` | `GET /accounts/{acct}` | `VerifyCredentials` — first call of every deploy |
| 2 | `Accounts.Subscriptions.Get` | `GET /accounts/{acct}/subscriptions` | `workersPlan`, the code entitlement |
| 3 | `Workers.Scripts.Update` | `PUT /accounts/{acct}/workers/scripts/{name}` | `putScript`, `putDurableObjectScript` |
| 4 | `Workers.Scripts.Delete` | `DELETE …/workers/scripts/{name}?force=true` | `deleteScript` — destroy, teardown |
| 5 | `Workers.Scripts.Settings.Get` | `GET …/workers/scripts/{name}/script-settings` | `FindApp` |
| 6 | `Workers.Scripts.ScriptAndVersionSettings.Get` | `GET …/workers/scripts/{name}/settings` | `scriptSettings`, bootstrap drift |
| 7 | `Workers.Scripts.Content.Get` | `GET …/workers/scripts/{name}/content/v2` | `deployedScript`, multipart readback |
| 8 | `Workers.Scripts.Secrets.List` | `GET …/workers/scripts/{name}/secrets` | bootstrap state |
| 9 | `Workers.Scripts.Secrets.Update` | `PUT …/workers/scripts/{name}/secrets` | `putBootstrapSecret` |
| 10 | `Workers.Scripts.Subdomain.Get` / `.New` | `GET`/`POST …/workers/scripts/{name}/subdomain` | `settleSubdomain`, `setSubdomain` |
| 11 | `Workers.Subdomains.Get` | `GET /accounts/{acct}/workers/subdomain` | `subdomainURL` |
| 12 | `Workers.Scripts.Assets.Upload.New` | `POST …/workers/scripts/{name}/assets-upload-session` | `uploadAssets` |
| 13 | `Workers.Assets.Upload.New` | `POST /accounts/{acct}/workers/assets/upload` | `uploadAssets`, JWT-authed batch |
| 14 | `Workers.Domains.List` / `.Delete` | `GET`/`DELETE /accounts/{acct}/workers/domains[/{id}]` | `detachCustomDomains` |
| 15 | `Zones.List` | `GET /zones?account.id=` | `accountZones`, `resolveZone` |
| 16 | `Workers.Routes.List/New/Update/Delete` | `…/zones/{zone}/workers/routes[/{id}]` | `reconcileWorkerRoutes`, preview wildcard, `BindDomain` |
| 17 | `DNS.Records.List/New/Update/Delete` | `…/zones/{zone}/dns_records[/{id}]` | `dnsWriter`, `addressRecordsAt` |
| 18 | `SSL.CertificatePacks.List` | `GET /zones/{zone}/ssl/certificate_packs` | `requireTLSCover` |
| 19 | `R2.Buckets.Get/New/Delete` | `…/accounts/{acct}/r2/buckets[/{name}]` | `cacheStore` |
| 20 | `R2.TemporaryCredentials.New` | `POST /accounts/{acct}/r2/temp-access-credentials` | `emptyBucket` |
| 21 | `User.Tokens.List/New/Verify/Delete` | `…/user/tokens[/…]` | `findToken`, `mintToken`, `awaitToken` |
| 22 | `User.Tokens.PermissionGroups.List` | `GET /user/tokens/permission_groups` | `permissionGroupID` |

That is 22 resources, ~30 distinct operations. Two of them are not JSON:

- **#3** is a `multipart/form-data` upload whose `metadata` part carries `main_module`,
  `compatibility_date`, `compatibility_flags`, `observability`, a `bindings` array spanning
  `assets` / `r2_bucket` / `worker_loader` / `service` / `plain_text` / `secret_text` /
  `durable_object_namespace` / `inherit`, an `assets.jwt` handle, and — for the bootstrap
  workers — a `migrations` block with `new_tag` and `new_sqlite_classes`
  (`stack.go:pendingMigrations`).
- **#7** answers `multipart/*` and ocel walks the parts to find `index.js`
  (`bootstrapplan.go:moduleContent`).

### Two things no base URL reaches

The REST client is not the only wire.

**The R2 S3 endpoint is hardcoded.** `r2.go` builds `https://%s.r2.cloudflarestorage.com`
from the account id in two places (`bootstrap`, `emptyBucket`) and hands it to the AWS SDK.
No option, no env var. A stand-in that served every REST call above would still send
`ListObjectsV2` / `DeleteObjects` to the real Cloudflare.

**The deployments store is called as a deployed worker, not as an API.** After bootstrap,
`store.go` issues plain `http.DefaultClient` requests to `endpoint + "/" + slug + subpath`
with a bearer secret — `/initialize`, `/staged`, `/promote`, `/history`, `/prune`,
`/remove-pointer`, `/version-stamp`, `/schema-version`, `/destroy`. That `endpoint` is
whatever `subdomainURL` returned, i.e. `https://<script>.<subdomain>.workers.dev`
(`cloudflare.go:709`, hardcoded scheme and suffix). So a stand-in must not only accept the
script upload, it must *run* the uploaded Durable-Object-backed worker and serve it at a URL
it can make ocel believe in. The API and the runtime are one problem, not two.

### `NewAt` is redundant, not the seam

The ticket says the base URL is "overridable in tests only (`cloudflare.go:74`, `NewAt`)".
Two corrections.

`NewAt` has **no callers at all** — not in tests either; `grep -rn NewAt` over the repo
returns only the definition. It is dead code.

And it is unnecessary. Every client ocel builds goes through `cf.NewClient`, and
`cloudflare-go/v4`'s `NewClient` applies `DefaultClientOptions()`, which reads
**`CLOUDFLARE_BASE_URL`** from the environment (`client.go:219-241`, verified in the pinned
`v4.6.0` module). So the REST base URL is *already* overridable, with no code change, for
the edge provider and for `NewDNS` alike. One env var on the runner and every one of the 22
calls above goes wherever you point it.

That makes the base URL the easy part, and the two wires above the hard part.
`CLOUDFLARE_BASE_URL` touches neither.

`NewAt` should be deleted whenever this area is next touched: it is unused code offering a
seam the SDK already provides.

## The prism experiment

**Live**, run on 2026-09-03 on this machine.

`cloudflare-go/v4@v4.6.0` is Stainless-generated and ships `scripts/mock`, which runs
`@stainless-api/prism-cli@5.8.5` against the OpenAPI spec named in `.stats.yml`:
`configured_endpoints: 1769`, spec at
`storage.googleapis.com/stainless-sdk-openapi-specs/cloudflare%2Fcloudflare-22bd279c….yml`
(last modified 2025-07-08, 12 MB unpacked). `scripts/test` starts it on `localhost:4010`
and points the SDK's own tests at it. That is the closest thing to an official Cloudflare API
mock, so it was tested first.

Fetched the spec, started `prism mock`, and drove it with a Go program using
`cloudflare-go/v4` the way `deploy/` does — same client, same params, same multipart writer.

| Call | Result |
| --- | --- |
| `Accounts.Get` | **401** — `Violation: request Invalid security scheme used` |
| `Subscriptions.Get` | 200 |
| `Scripts.Settings.Get` | 200 |
| `ScriptAndVersionSettings.Get` | 200 |
| `Scripts.Content.Get` | **crashes the server** |
| `Scripts.Update` (multipart) | **422** — `Violation: request.body.metadata … must be object` |
| `Assets.Upload.New` | 200 |
| `Scripts.Subdomain.New` | 200 |
| `Workers.Subdomains.Get` | 200, `subdomain = my-subdomain` |
| `R2.Buckets.New` | 200 |
| `R2.TemporaryCredentials.New` | 200 |
| `Scripts.Secrets.List` | 200 |
| `Scripts.Delete` | 200 |

Three failures, each fatal on its own.

**The credential check cannot pass.** `GET /accounts/{id}` in Cloudflare's published spec
carries only the legacy `X-Auth-Email` + `X-Auth-Key` scheme. Confirmed by curl: the same
path answers 401 with a bearer and 200 with the email/key pair. `cloudflare-go` sends a
bearer, so ocel's first call of every deploy is a hard 401 against Cloudflare's own spec.

**The script upload is rejected.** The spec models the multipart `metadata` field as an
`object`; a multipart part is bytes, and prism's validator refuses it. The one call the
entire deploy exists to make cannot be made.

**Reading a script's content kills the process.** The spec declares that response's media
type as the literal string `string`; prism's content-type parser throws
`TypeError: invalid media type` out of `content-type/index.js:126` and the server exits.
Not a bad response. No server left.

**And it is stateless anyway.** Two `POST /zones/{z}/workers/routes` with different patterns
both returned the same canned `023e105f4ecef8ad9ca31a8372d0c353` / `example.com/*` /
`my-workers-script`, and the subsequent list still showed only the example route. ocel's
route reconciliation reads its own writes back — `ensureRoute` matches on pattern,
`pruneStaleRoutes` deletes what it does not recognise, `DomainOwner` answers from the list.
Against canned examples it would attach the same route forever and then try to prune it.

A fourth failure sits one call further on, found independently against the same spec:
`POST /accounts/{id}/workers/assets/upload` answers **401**, because the operation declares
`security: [{assets_jwt: []}]` and `assets_jwt` is never defined in
`components.securitySchemes` — the only schemes are `api_email`, `api_key`, `api_token` and
`user_service_key`. No credential can satisfy it. The same defect is in the published
`cloudflare/api-schemas` spec, so it is upstream, not a v4-generation artifact.

Three of the four failures are patchable in about six lines of the spec JSON — type
`metadata` as a string, define `assets_jwt` as bearer, fix the `content/v2` response keyed
under a media type literally named `string` — and patched, the script upload returns 200 and
the asset upload 201.

That is the tempting conclusion, and it is the wrong one. Patching Cloudflare's spec so the
mock accepts a request Cloudflare's real API accepts means the harness is now asserting
against a document we edited. And it fixes none of the statelessness: the deploy would walk
its full call sequence and every read-back would still return `example.com/*`.

Prism gives schema-valid noise. ocel needs a state machine.

Nothing here is prism's fault. It is a stateless example-responder driven by a schema, and
Cloudflare says so themselves when they endorse mocking from the spec: it exists so "you
aren't violating Cloudflare's API contract, but without needing to worry about specifics of
managing real resources"
([the open-API transition post](https://blog.cloudflare.com/open-api-transition/)). ocel's
edge deploy is entirely about managing real resources. The same verdict covers WireMock and
Mockoon pointed at [`cloudflare/api-schemas`](https://github.com/cloudflare/api-schemas)
(BSD-3, pushed today, 24.8 MB `openapi.json`) — same spec, same statelessness, same three
failures.

## Everything in workers-sdk is a client or a runtime

Checked against a clone of `cloudflare/workers-sdk@main` on 2026-09-03: `wrangler@4.128.0`,
`miniflare@5.20260831.0-alpha`, `workerd@1.20260902.1`.

The decisive fact is one grep: `workers/scripts` appears in `packages/*/src` only at
outbound `fetchResult(...)` call sites — `deploy-helpers/src/deploy/deploy.ts`,
`helpers/versions-api.ts`, `helpers/assets.ts`. Never a route handler. Nothing in the
repository *serves* `client/v4`.

| Candidate | What it is | Serves | Base URL enough? |
| --- | --- | --- | --- |
| `wrangler dev` local mode | workerd + three local ports | none of the 22 | no |
| `wrangler deploy --dry-run` | a bundler that stops before auth | none | no — see below |
| `unstable_dev` | in-process JS handle on local workerd | none | no |
| `getPlatformProxy` | in-process `{env, cf, ctx, caches}` objects | none | no — there is no port |
| dev registry | a watched directory of JSON files | none | no |
| miniflare programmatic API | workerd + binding simulators | none of the REST calls; **two of the S3 calls** | no |
| msw mocks in workers-sdk | in-process interception, binds no port | none, from outside | no |

Details worth keeping:

**`wrangler dev` binds three surfaces, none of them the API.** The user worker (8787), the
inspector (9229) — which is Chrome DevTools Protocol, not REST — and **Local Explorer**, at
`/cdn-cgi/local/explorer/api`. That last one is the near-miss both surveys converged on: it
reuses Cloudflare's own `operationId`s and response envelopes and its own OpenAPI document
calls it "a local subset of the Cloudflare API". But its paths are KV values, D1 queries, R2
*objects*, DO namespaces and Workflows. No `client/v4` prefix, no `/accounts/{id}` segment,
no script upload, no routes, no zones, no DNS. It pokes a running worker's resources; it
cannot create one.

**`--dry-run` is already ocel's build step, and that is the right use for it.** Each of
`entry/`, `deployments-store/` and `isr-writer/` builds with
`wrangler deploy --dry-run --outdir=dist` (their `package.json`s), and wrangler ^4.110.0 is
already a devDependency. Cloudflare: "Compile a project without actually deploying to live
servers." In wrangler's source the account id is `undefined` on a dry run and
`preUploadApiChecks()` is skipped outright. So it never contacts the API, which is exactly
why it cannot stand in for one. Its one further use here: `--outfile` emits the exact
multipart body a real deploy would `PUT`, which is a differential oracle for ocel's own
`buildScriptMultipart` if that is ever in doubt.

**`unstable_dev` is deprecated**, superseded by `createTestHarness()`; with `local: false` it
needs a real account anyway.

**`getPlatformProxy` has no HTTP surface at all.** It returns JS objects. Worth flagging
for anyone who reaches for it in a harness: `remoteBindings` defaults to true, and it will
call `.../workers/subdomain/edge-preview` against the **real** API with real credentials
unless passed `remoteBindings: false`.

**The dev registry's HTTP server no longer exists.** Removed in wrangler 3.102.0, "Remove
the server-based dev registry in favour of the more stable file-based dev registry". It is
now one JSON file per worker under `~/.config/.wrangler/registry`, watched by chokidar, with
cross-process calls over workerd's debug port via Cap'n Proto. Port 6284 appears nowhere in
current source.

**The msw mocks cover the right endpoints and are still unusable.**
`packages/wrangler/src/__tests__/helpers/msw/` covers script upload, versions, deployments,
script-settings, assets-upload-session, assets upload, workers subdomain (including the
10007 "not registered" failure), zones, routes, R2 buckets and `/user/tokens/verify` — a
better endpoint list than anything else found. But `setupServer` patches Node's
`http`/`https`/`fetch` in-process; `msw.listen()` **binds no port**, so a Go binary cannot
reach it under any configuration. It is also unpublished (`files` in wrangler's
`package.json` ships no `src/__tests__/`). And extracting it is a rewrite, not an
extraction: the handlers are *assertions* (`assert(params.accountId === "some-account-id")`,
`expect(body).toEqual(...)`), most are `{once: true}`, they are registered at roughly 1100
ad-hoc `msw.use(...)` call sites rather than a table, and there is no state anywhere — the
script handler synthesises a body from the name in the URL. You would strip the assertions,
drop the once-semantics, gather the call sites, and then write the state machine that was
never there.

**One genuine find, and it is not on the REST side.** Miniflare now serves a real
SigV4-authenticated S3-compatible endpoint at `/cdn-cgi/local/r2/s3`, and it implements
**`ListObjectsV2` and `DeleteObjects`** — both of ocel's S3-protocol calls — alongside
`GetObject`, `PutObject` and multipart. Confirmed in
[`detect.worker.ts`](https://raw.githubusercontent.com/cloudflare/workers-sdk/main/packages/miniflare/src/workers/r2/s3/detect.worker.ts).
`CreateBucket` and `DeleteBucket` are deliberately *not* implemented — that file's own
comment: "both are implemented by real R2, but local buckets are statically configured, so
they get the templated named error instead". Configured through
`r2_buckets[].local_dev.experimental_s3_credentials`; path-style addressing required; the
AWS SDK's `addExpectContinueMiddleware` must be removed because workerd never sends
`100 Continue`. It is undocumented on developers.cloudflare.com. Source is the only
reference. This shrinks the emulator's item 5 below, without removing it.

## The real-account route

The alternative to a stand-in is a real Cloudflare account on the runner. It fails on three
independent counts, before fidelity is even discussed.

**The `worker_loader` binding is paid-only.** `bindCodeLoader` puts a `worker_loader`
binding on *every* app worker and on the preview entry worker
(`cloudflare.go:bindCodeLoader`, `previewwildcard.go:previewEntryWorker`). Cloudflare:
"Dynamic Workers are currently only available on the Workers Paid plan"
([Dynamic Workers pricing](https://developers.cloudflare.com/dynamic-workers/pricing/)).
ocel already knows this shape of failure. `workersPlan` exists to warn that "an account on
the Workers Free plan is rejected by Cloudflare when the worker is uploaded, after the
deploy has begun changing your infrastructure". A free CI account walks straight into it.

**Fork PRs get no secrets, and no switch changes that.** GitHub: "With the exception of
`GITHUB_TOKEN`, secrets are not passed to the runner when a workflow is triggered from a
forked repository", and that token "has read-only permissions in pull requests from forked
repositories"
([use secrets](https://docs.github.com/en/actions/how-tos/write-workflows/choose-what-workflows-do/use-secrets)).
The three loosening switches — run fork workflows, send write tokens, send secrets — are
private-repository-only settings; a public repo has no toggle that hands a fork PR a
Cloudflare token. Approving a first-time contributor's run unblocks the run, it does not
release secrets. `pull_request_target` does release them, and GitHub's own page for it
describes the attack: "An attacker only needs to open a pull request from a fork whose
Makefile … contains malicious commands. Those commands then run with the base repository's
secrets and token"
([securely using pull_request_target](https://docs.github.com/en/actions/reference/security/securely-using-pull_request_target)).
That is a per-PR arbitrary-code-execution surface bought to make one matrix cell green.

**The edge cannot reach the origin as ocel addresses it.** The worker fetches the origin at
the URL `Resolver.FunctionURL` baked in at assemble time. On floci that is
`http://<id>.lambda-url.us-east-1.localhost:4566/` ([#831](https://github.com/ocelhq/ocel/issues/831)) —
a `localhost` name on a plain-HTTP port. A worker running in Cloudflare's network resolves
that to Cloudflare's own loopback, not the runner's. Bridging it means a
[TryCloudflare quick tunnel](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/do-more-with-tunnels/trycloudflare/),
which is account-free but which Cloudflare documents as "intended for testing and
development only", with "no … SLA or uptime", a hard cap of "200 in-flight requests"
returning 429 above it, and no Server-Sent Events. It would also mean rewriting the origin
URL the harness never otherwise touches, a special case for one target, which
[#830](https://github.com/ocelhq/ocel/issues/830) forbids.

Three further frictions, none fatal alone: R2 bucket creation needs a one-time manual
dashboard checkout ("Complete the checkout flow to add an R2 subscription to your account",
[R2 get started](https://developers.cloudflare.com/r2/get-started/)), so CI cannot bootstrap
it; routes, DNS records and certificate packs all need an active zone, which means a real
domain and a shared mutable namespace across concurrent PRs; and the account-wide API limit
is 1,200 requests per 5 minutes, after which *every* call 429s for five minutes
([API limits](https://developers.cloudflare.com/fundamentals/api/reference/limits/)).
One deploy is ~30 calls, so the ceiling is real but not near.

What does work on free, for the record: script upload over REST, static assets, workers.dev
subdomains, observability, and Durable Objects — but "Only Durable Objects with SQLite
storage backend", which is exactly what ocel's store declares (`new_sqlite_classes`,
`stack.go:pendingMigrations`)
([DO pricing](https://developers.cloudflare.com/durable-objects/platform/pricing/)).
The blocker is `worker_loader`, not the storage engine.

## Nothing else exists

Searched GitHub code and repo search, and the npm registry, for a runnable server speaking
`api.cloudflare.com/client/v4`. `"api.cloudflare.com/client/v4" mock` and
`"cloudflare" "client/v4" mock server` both return zero results; three npm searches return
no package implementing the API.

The one near-miss is LocalStack's community
[`miniflare` extension](https://github.com/localstack/localstack-extensions) — a real HTTP
server at `localhost:4566/miniflare` that parses the multipart bundle on
`PUT /accounts/{id}/workers/scripts/{name}` and boots miniflare with it, plus stubs for
`/user`, `/memberships`, `workers/subdomain`, `workers/deployments/by-script` and secrets.
It is the right idea. It is also an extension, not LocalStack core (LocalStack proper has no
Cloudflare support), its last functional commit is 2024-11-23, its README calls it
experimental, and it covers none of R2, routes, DNS, zones, tokens, certificate packs or
custom domains. Of ocel's 22 resources it serves perhaps five, and it would have to be
adopted as an unmaintained fork.

Two dead hobby repos round it out —
[`fake-cloudflare-api`](https://github.com/clementreiffers/fake-cloudflare-api) (archived,
2023-07-12) and [`fake-cf-api`](https://github.com/clementreiffers/fake-cf-api)
(2023-07-14), both zero-star, both script-upload-and-secrets only.

Cloudflare's own
[Local Explorer](https://developers.cloudflare.com/workers/local-development/local-explorer/)
(Wrangler 4.82.1+) describes itself as "a local mirror of the Cloudflare API", which is
promising until you read what it mirrors: KV pairs, R2 *objects*, D1 rows, DO SQLite rows,
Workflow runs. It is a data-plane inspector at `/cdn-cgi/explorer/api`, not `client/v4`, and
it uploads no script, creates no bucket, writes no route.

## What an emulator would have to be

Sizing it honestly, because "build an emulator" is only a real option if its shape is known.

It is not one server. It is:

1. A stateful `client/v4` server over the 22 resources in the table — zones, routes, DNS
   records, scripts, secrets, subdomains, assets sessions, R2 buckets, tokens, permission
   groups, certificate packs — that reads its own writes back, because ocel's reconcilers
   list before they act.
2. A multipart parser for the script upload, understanding `main_module`, the eight binding
   types ocel emits, and Durable Object `migrations` with `new_sqlite_classes` — and the
   matching multipart *writer* for `content/v2`, since `deployedScript` diffs the bytes it
   gets back against the bundle it built.
3. A miniflare instance per uploaded script, wired with the bindings the metadata declared,
   plus a router in front honouring worker route patterns including the preview wildcard —
   which is exactly [#832](https://github.com/ocelhq/ocel/issues/832), already answered
   green, and already half-configured: each of `entry/`, `deployments-store/` and
   `isr-writer/` ships a `wrangler.jsonc` declaring the same bindings the REST upload
   would create.
4. A workers.dev-shaped URL per script that ocel's own store client can reach, because
   `subdomainURL` hardcodes `https://<script>.<subdomain>.workers.dev` and `storeRequest`
   dials it directly.
5. An S3-protocol endpoint for R2 objects, reachable at
   `https://<account>.r2.cloudflarestorage.com`, since that host is hardcoded. Miniflare's
   `/cdn-cgi/local/r2/s3` already implements both calls ocel makes, so this is a wiring
   problem rather than an implementation one — but only for objects: it deliberately
   refuses `CreateBucket` and `DeleteBucket`, which the bootstrap and teardown need, so
   those still fall to item 1.

Items 4 and 5 are the ones that make it more than a weekend, and for the same reason:
neither is reachable by any base URL. `CLOUDFLARE_BASE_URL` moves the REST client and
nothing else. Item 4 needs `subdomainURL` to stop composing
`https://<script>.<subdomain>.workers.dev`; item 5 needs the R2 S3 endpoint to stop being a
format string over the account id. Both are ocel changes, and both are *features* — which
the map's no-features rule puts out of bounds for this week.

## Recommendation

**Stay red at `up` on floci, with the `cloudflare` cell green on dispatch only.**

Nothing existing stands in. Cloudflare's own spec-driven mock rejects the script upload,
401s the credential check and crashes on `content/v2`; every runtime emulator, ocel's
`--dry-run` build step included, is on the wrong side of the wire; the one community server
that parses a script upload is an unmaintained extension covering five of 22 resources; and
the real-account route needs a paid plan for `worker_loader`, secrets a fork PR cannot have,
and a tunnel Cloudflare documents as testing-only.

The base URL turned out to be the easy half. `CLOUDFLARE_BASE_URL` already works, with no
code change and no `NewAt`. It is the other half that has no answer: the R2 S3 endpoint and
the deployments-store call to `<script>.<subdomain>.workers.dev` are composed from hardcoded
hostnames, so even a perfect `client/v4` server would not complete a deploy.

The one thing that is worth doing and is not a stand-in: `CLOUDFLARE_BASE_URL` makes the
prism mock cheap enough to point ocel at deliberately, as a **wire-format check** — does the
binary marshal, authenticate, paginate and decode errors the way Cloudflare's own spec says?
That is a unit-level assertion about ocel's client, not a journey, and it belongs nowhere
near the `example × target` grid. Noted for later; not proposed as work now.

So the cell is red at `up`, with this file as the issue the expectations entry links, which
is what [#830](https://github.com/ocelhq/ocel/issues/830) prescribes for a gap: "a cell that
fails is red in the expectations file with a linked issue naming the gap, and the fix flips
it green." The map already prescribes this outcome; taking it is not a concession.

**Do not file the emulator issue yet.** The ticket offers it as the outcome if every
existing tool is ruled out, and every existing tool is ruled out — but the emulator's cost
is now known, and it is not one issue. Its runtime half is
[#832](https://github.com/ocelhq/ocel/issues/832), already answered green with the
`wrangler.jsonc` files already on disk. Its control-plane half needs two ocel changes that
are features by the map's own definition (a configurable R2 S3 endpoint, and a
`subdomainURL` that stops composing `workers.dev` hostnames), so it cannot be built in a
no-features week regardless. File it when the suite is running and the `cloudflare` column
is the one thing left red — by then #832's harness will have shown what the router and
bindings actually need, and the issue can be written against evidence rather than against
this estimate.

The dispatch run is unaffected: it already has a real account, a real zone and a real
origin, and the ticket's own framing ("the cell green on dispatch only") is where the
Cloudflare edge gets its coverage.
