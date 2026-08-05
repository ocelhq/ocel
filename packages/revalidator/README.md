# @ocel/revalidator

The account's ISR revalidation consumer: one SQS FIFO message becomes one
SigV4-signed `HEAD` at the app's origin, which renders the route and writes it
through its own cache handler. The queue has already deduplicated every other
colo that asked for the same render; this function is what turns the survivor
into a render, at a concurrency the event source mapping caps.

It understands no framework. The edge adapter prepares the force-render headers
and declares the receipt it expects back; the consumer signs what it was handed
and evaluates only that declared expectation.

## The message names no host

Every record carries the app's bypass token in `x-prerender-revalidate`, so
where the trigger is sent is a security decision. The message does not make it.
It names a route — `isrPrefix` (which deploy) and `routeId` (which of that
deploy's functions serves it) — and the consumer looks the origin up in the
record the deploy itself wrote:

```
s3://<OCEL_ASSET_BUCKET>/<isrPrefix>/origin.json

{ "v": 1, "functionUrls": { "<routeId>": "https://<id>.lambda-url.<region>.on.aws/" } }
```

That is the same `routeId → Function URL` map the deploy already hands the edge
in its Cloudflare deployment record, written to the one place keyed by
`isrPrefix` that the consumer's own account can read.

Resolving rather than validating is what makes the exfiltration class
impossible instead of merely checked. A compromised edge key can enqueue any
message it likes; the worst it can name is a `routeId` this deploy recorded, or
one it did not, and the second is a rejection. There is no allowlist to keep
current, and no pattern that would admit another AWS customer's Function URL.
The route path is joined onto the recorded origin and the result's origin is
compared back to it, so a route path that tries to be a URL cannot become one.

The record is read once per `isrPrefix` per invocation — a batch of ten is
usually one — and never across invocations: a container that outlived a
redeploy must not keep triggering the origin the previous build recorded.

`x-forwarded-host` stays in the message. The consumer resolves the *origin*,
which is the Function URL; the app's *public* hostname is route knowledge only
the edge has, and the rendered entry's absolute URLs depend on it. It names no
destination, so it carries no part of the decision above.

## Environment

`.24` renders these onto the function.

| Variable | Required | Meaning |
| --- | --- | --- |
| `OCEL_ASSET_BUCKET` | yes | The substrate's asset bucket, holding each deploy's `<isrPrefix>/origin.json`. Same variable and same bucket the tag publisher reads. Unset ⇒ the consumer resolves nothing and triggers nothing. |
| `AWS_REGION` | Lambda | The bucket's region, used to address and sign the record read. |
| `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` / `AWS_SESSION_TOKEN` | Lambda | The function role's credentials. The message carries none. |

The environment is read per invocation, so rotated role credentials are the ones
the next batch signs with.

## Resource contract

The values `.24` must render, and why the package expects them. The numbers live
in `src/limits.mts`; the arithmetic between them is asserted in
`test/limits.test.mts`, because it is what breaks silently when one of them
moves.

| Setting | Value | Why |
| --- | --- | --- |
| Function timeout | **150s** | A batch is 10 records processed in order, each paying a 10s trigger budget and, in the worst case of ten distinct deploys, a 2s record read: 120s, plus cold start and signing. |
| Batch size | 10 | With `ReportBatchItemFailures`. |
| `MaximumConcurrency` | 10 | The global render-drain bound. A `ScalingConfig` **sub-property** in CloudFormation, not a top-level event-source-mapping property. |
| Queue `VisibilityTimeout` | 300s | Must exceed the function timeout, or records already processed are redelivered. |
| Queue `MessageRetentionPeriod` | 300s | A revalidation older than the dedup window is worthless. |

### What `.24` must add for the resolution above to work

- **IAM, on the revalidator role**, alongside its SQS and `lambda:InvokeFunctionUrl`
  grants:

  ```yaml
  - Effect: Allow
    Action: s3:GetObject
    Resource: !Sub '${AssetBucket.Arn}/*'
  ```

  Read-only, and no `s3:PutObject` — the consumer never writes to the store.
- **Env**, `OCEL_ASSET_BUCKET: !Ref AssetBucket`, exactly as `publisher.go`
  renders it for the tag publisher.
- **A deploy-side write** (`cloud/aws/deploy`), which does not exist yet: after
  the app stack's outputs are read, `PutObject` `<isrPrefix>/origin.json` into
  the asset bucket with the `{v, functionUrls}` document above. The map is the
  one `buildDeploymentRecord` already computes via `appFunctionURLsByRoute`; the
  Function URL is only knowable after `up`, which is why bootstrap cannot render
  it and the deploy must write it. A deploy predating this write answers 404 and
  its routes fail to resolve — visible in the logs as `origin-unusable`, never
  as a silent success.

## Handler contract

Per record, in the batch's order:

- Parse the message; an unknown `v` or a malformed body is an item failure.
- Resolve the origin from `<isrPrefix>/origin.json` and compose the trigger URL
  from it and `routePath`. An unreadable record is `origin-unavailable`
  (transient — redelivery is worth something); a record that does not answer for
  this route, or a route path that would leave the recorded origin, is
  `origin-unusable`; no configured bucket is `origin-unconfigured`. All three
  are item failures, and none of them fetches the origin.
- `HEAD` the resolved URL with the message's headers. The headers are signed
  along with `host`: nothing sits inside the TLS session to rewrite them, so
  signing them is free and narrows what a captured signature could carry.
- `ok` with no declared expectation, or with the declared header matching, is a
  success. `ok` with the expectation missing or mismatched is **also a success**,
  logged as `RevalidateExpectMiss`: the route went dynamic since it was enqueued
  and redelivering it cannot help.
- Non-ok, a thrown fetch, or the 10s timeout is an item failure.
- FIFO: the first failure in a message group stops that group for the batch, and
  every later record of that group is reported unprocessed and logged
  `RevalidateSkipped` — a record can reach the DLQ having never been tried, and
  the log is what says which happened. A record with no group attribute is its
  own group, keyed by its message id. Other groups run on. Nothing throws out of
  the handler — a thrown handler fails the whole batch.

Logs carry the dedup ingredients (`isrPrefix`, `routePath`, `lastModified`,
`enqueuedAt`) plus the message id and an outcome code. They never carry the
record, its headers, or an error's own text — the emitter has no field that
could hold them.

## Release

`pnpm --filter @ocel/revalidator zip` builds a reproducible
`dist/revalidator.zip` (fixed timestamps, sorted entries). It is published as a
GitHub release asset `revalidator-v<version>` and pinned by version + sha256 in
`cloud/aws/bootstrap/revalidatorversion.go`, which bootstrap verifies
fail-closed. Unpinned, bootstrap renders no consumer at all.
