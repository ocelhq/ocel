# @ocel/revalidator

The account's ISR revalidation consumer: one SQS FIFO message becomes one
SigV4-signed `HEAD` at the app's origin, which renders the route and writes it
through its own cache handler. The queue has already deduplicated every other
colo that asked for the same render; this function is what turns the survivor
into a render, at a concurrency the event source mapping caps.

It understands no framework. The edge adapter prepares the whole trigger — URL,
force-render header, and the receipt it expects back — and the consumer signs
what it was handed and evaluates only that declared expectation.

## Environment

`.24` renders these onto the function.

| Variable | Required | Meaning |
| --- | --- | --- |
| `OCEL_REVALIDATE_ALLOWED_HOSTS` | yes | Comma-separated **exact** Function URL hosts this consumer may trigger, e.g. `abc.lambda-url.us-east-1.on.aws,def.lambda-url.eu-west-1.on.aws`. Case-insensitive, blanks ignored. |
| `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` / `AWS_SESSION_TOKEN` | Lambda | The function role's credentials. The message carries none. |

`OCEL_REVALIDATE_ALLOWED_HOSTS` is the security boundary, not a convenience.
Every record carries the app's bypass token in `x-prerender-revalidate`, so a
message naming a host outside this list would hand that token — plus a valid
SigV4 signature — to whoever owns the host. Validating the *shape* of the host
(`*.lambda-url.*.on.aws`) would admit any AWS customer's Function URL, so the
allowed hosts are pinned by configuration the edge cannot reach. Unset or empty
permits nothing: the consumer triggers nothing rather than anything.

The environment is read per invocation, so rotated role credentials are the ones
the next batch signs with.

## Resource contract

The values `.24` must render, and why the package expects them:

| Setting | Value | Why |
| --- | --- | --- |
| Function timeout | **120s** | A batch is 10 records processed in order, each with a 10s trigger budget: 100s worst case, plus signing and cold-start slack. |
| Batch size | 10 | With `ReportBatchItemFailures`. |
| `MaximumConcurrency` | 10 | The global render-drain bound (`ScalingConfig`). |
| Queue `VisibilityTimeout` | 300s | Must exceed the function timeout, or records already processed are redelivered. |
| Queue `MessageRetentionPeriod` | 300s | A revalidation older than the dedup window is worthless. |

## Handler contract

Per record, in the batch's order:

- Parse the message; an unknown `v`, a malformed body, or a host outside the
  permitted set is an item failure, and the host check happens before any fetch.
- `HEAD` the message's URL with exactly its headers plus the SigV4 headers.
  Only `host` is signed, so a header rewritten in transit cannot invalidate the
  signature.
- `ok` with no declared expectation, or with the declared header matching, is a
  success. `ok` with the expectation missing or mismatched is **also a success**,
  logged as `RevalidateExpectMiss`: the route went dynamic since it was enqueued
  and redelivering it cannot help.
- Non-ok, a thrown fetch, or the 10s timeout is an item failure.
- FIFO: the first failure in a message group stops that group for the batch, and
  every later record of that group is reported unprocessed. Other groups run on.
  Nothing throws out of the handler — a thrown handler fails the whole batch.

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
