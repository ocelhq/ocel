# Runbook — cut, pin and prove the ISR revalidator on a live substrate

The live half of `ocelhq-wvag.27`. Everything offline is already done and committed on
`isr-herd/18-revalidator-pin-live-e2e`; what is left is the release, the pin, the deploy
and the observations. **This document is the whole procedure — nothing below needs to be
re-derived from the spec.**

Spec of record: `docs/research/isr-queue-revalidation-design.md` (read §0a's amendment
index first; §9 is the acceptance list this runbook executes). Package contract:
`platform/aws/functions/revalidator/README.md`. Prior live runs and the traps they hit:
`docs/handoffs/isr-thundering-herd.md`, sections `.14`, `.15`, `ocelhq-yo9b`.

## 0. What is already established, and what is not

| Fact | Established how |
| --- | --- |
| The artifact builds reproducibly | `pnpm --filter @platform/aws-revalidator zip` run three times from a clean `dist/`, byte-identical each time |
| Its digest | `2f830a670b3fbc9f313018375cb2f1d88f6b5950e986373079d212548ca8a0dd`, 5843 bytes |
| `ensureArtifact` refuses a mismatch | Run against the real pin with corrupted bytes: zero `PutObject`s, error text in §2 below. Regression tests: `TestEnsureRevalidatorArtifact_RefusesADigestMismatch` / `_UploadsAVerifiedArtifact` |
| The release `revalidator-v0.0.1` | **DOES NOT EXIST.** `gh release list` shows only `tag-publisher-v0.0.1` and two `image-optimizer` tags |
| The pin | **UNSET, deliberately.** See §2 |

Everything in §§1–3 mutates state outside this repo and needs the human authorization
listed on the bd issue.

## 1. Preconditions — verify all of these before anything else

```bash
# AWS: must be 363236815301
aws sts get-caller-identity --query Account --output text

# Cloudflare. `wrangler whoami` RETURNS EMPTY EVEN WITH A VALID TOKEN — do not use it.
export CLOUDFLARE_API_TOKEN=<token>
export CLOUDFLARE_ACCOUNT_ID=a1731fc73cb2bf6b2979c98033012ca8   # account "Ocel"
curl -s -o /dev/null -w '%{http_code}\n' \
  -H "Authorization: Bearer $CLOUDFLARE_API_TOKEN" \
  https://api.cloudflare.com/client/v4/user/tokens/verify        # expect 200

# GitHub, for the release
gh auth status                                                   # needs repo scope
```

A fresh session starts without the Cloudflare variables; a missing token fails in ways
that read as a misconfiguration rather than as a missing credential.

## 2. Cut the release and set the pin

The build is deterministic, so re-derive the digest rather than trusting this document:

```bash
rm -rf platform/aws/functions/revalidator/dist
pnpm --filter @platform/aws-revalidator zip
sha256sum platform/aws/functions/revalidator/dist/revalidator.zip
# expect 2f830a670b3fbc9f313018375cb2f1d88f6b5950e986373079d212548ca8a0dd
```

Publish it as asset `revalidator.zip` on tag `revalidator-v0.0.1` (the URL bootstrap will
ask for is `revalidatorReleaseURL` in `platform/aws/provider/bootstrap/revalidator.go`):

```bash
gh release create revalidator-v0.0.1 \
  platform/aws/functions/revalidator/dist/revalidator.zip \
  --title 'revalidator v0.0.1' --notes '<...>'
```

**Then hash the published asset, not the local build** — that download is the only thing
that proves the release carries the reviewed bytes:

```bash
curl -sL https://github.com/ocelhq/ocel/releases/download/revalidator-v0.0.1/revalidator.zip \
  | sha256sum
```

Only if that matches, apply the pin. This is the entire diff, in
`platform/aws/provider/bootstrap/revalidatorversion.go`:

```diff
-	RevalidatorArtifactVersion = ""
+	RevalidatorArtifactVersion = "0.0.1"
-	RevalidatorArtifactSHA256 = ""
+	RevalidatorArtifactSHA256 = "2f830a670b3fbc9f313018375cb2f1d88f6b5950e986373079d212548ca8a0dd"
```

The comment block above those constants also carries the "release is outstanding" framing;
update it to the pinned framing the way `publisherversion.go` reads after `.14`.

Then `cd platform/aws/provider && go test ./...` — `TestRevalidator_UnpinnedRendersNoConsumerAndNoQueueURL`
uses fixture pins, not the real ones, so it must still pass after the pin lands. If it
fails, a template test is reading the shipped pin and that is a defect, not a rebaseline.

**The fail-closed check, for reference.** A digest mismatch produces exactly this, with
zero `PutObject`s and no `artifactCode`:

```
revalidator artifact https://github.com/ocelhq/ocel/releases/download/revalidator-v0.0.1/revalidator.zip
has sha256 <computed>, but this build requires
2f830a670b3fbc9f313018375cb2f1d88f6b5950e986373079d212548ca8a0dd; refusing to deploy it
```

A verified artifact lands at key `ocel-revalidator/0.0.1-<digest>.zip` in the account's
artifact bucket, uploaded with the pin as S3's own `ChecksumSHA256`.

## 3. Rebuild the provider BEFORE any deploy — this is not optional

```bash
make provider
make cli lib
```

**The prebuilt provider binary at `packages/native-lib/provider-aws-linux-x64/bin/deploy`
can predate your change and still carry the old value baked in, so a deploy from a tree
that contains the pin silently tests the last build.** This nearly produced a false
negative on `ocelhq-yo9b`: the verification deploy attached the *old* membrane layer from
a tree that contained the fix. Editing a Go file does not change what `ocel deploy` runs.

Verify the binary is actually newer than the pin edit before deploying:

```bash
ls -l --time-style=full-iso packages/native-lib/provider-aws-linux-x64/bin/deploy \
  platform/aws/provider/bootstrap/revalidatorversion.go
```

## 4. Bootstrap, and watch decision F's transition

Human decision F: `OCEL_REVALIDATE_QUEUE_URL` reaches the worker **only when the consumer
is rendered**, i.e. only when the pin is present. The transition is the observation, not
the end state — capture the before as well as the after.

```bash
STACK=<the substrate stack name>          # production and preview are separate stacks

# BEFORE the pinned bootstrap: the queue exists, the consumer does not, no output.
aws cloudformation describe-stacks --stack-name "$STACK" \
  --query 'Stacks[0].Outputs[?OutputKey==`RevalidateQueueUrl`]'    # expect []
aws sqs get-queue-url --queue-name ocel-revalidate.fifo            # expect a URL

ocel bootstrap                            # or `ocel bootstrap --preview`

# AFTER: the output appears and the consumer exists.
aws cloudformation describe-stacks --stack-name "$STACK" \
  --query 'Stacks[0].Outputs[?OutputKey==`RevalidateQueueUrl`].OutputValue' --output text
aws cloudformation describe-stack-resources --stack-name "$STACK" \
  --query 'StackResources[?starts_with(LogicalResourceId, `Revalidator`)].[LogicalResourceId,PhysicalResourceId,ResourceStatus]' \
  --output table
```

Expected new logical resources: `Revalidator`, `RevalidatorRole`,
`RevalidatorQueueConsumer`. There are deliberately no CloudWatch alarms — the bootstrap
stack must cost nothing to leave idle. The Lambda has no `FunctionName` property, so CloudFormation
generates its physical name — read it from the table above and use it everywhere below;
do not guess it.

Prove the deployed code is the reviewed artifact, byte for byte (this is how `.15` proved
the publisher):

```bash
REVALIDATOR=<physical function name>
aws lambda get-function-configuration --function-name "$REVALIDATOR" \
  --query '[CodeSize,Environment.Variables]'
# CodeSize must be 5843, and OCEL_ASSET_BUCKET must be set to the substrate's asset bucket
```

Then confirm the worker env actually received the URL — the CFN output is the source, the
worker binding is the effect, and the two have been out of step before:

```bash
curl -s -H "Authorization: Bearer $CLOUDFLARE_API_TOKEN" \
  "https://api.cloudflare.com/client/v4/accounts/$CLOUDFLARE_ACCOUNT_ID/workers/scripts/<script>/settings" \
  | jq '.result.bindings[] | select(.name=="OCEL_REVALIDATE_QUEUE_URL")'
```

**`sqsRegion` is strict about the queue URL's shape** (`platform/edge/cloudflare/workers/entry/src/signing.ts`,
`labels[0] === "sqs"`). A FIPS or dualstack endpoint yields no sender at all and the whole
path is silently inert — no queue bound, every refresh renders as before. Read the actual
output value and confirm it is the plain regional form
`https://sqs.<region>.amazonaws.com/<account>/ocel-revalidate.fifo`. If it is anything
else, stop: the run cannot conclude anything about the queue.

## 5. Deploy the app and seed the route

Use `scripts/e2e-next/smoke-app` (its `app/isr/page.tsx` has `revalidate = 5` and renders
a fresh token per render; `app/golden/page.tsx` is the golden probe). See
`scripts/e2e-next/README.md` for the sidecar and preview-domain setup.

**Two routes must be added to the smoke app before this run** — the review items in §8
cannot be exercised without them:

1. A **header-varying** prerender-capable route: one whose rendered bytes depend on an
   `allowHeader`-filtered client header or on the host (e.g. it renders an absolute URL
   built from `x-forwarded-host`). This is what makes §8.1 falsifiable.
2. A **query-string** route: a prerender-capable route that renders `?q=` into its body.
   This is what makes §8.3 falsifiable.

After the deploy, confirm the deploy wrote the origin record — an absent record is a build
whose routes enqueue and never revalidate, and it reports as `origin-unusable` in the
consumer logs rather than as a deploy failure:

```bash
ISR_PREFIX=<env>/<project>/<app>/<buildId>    # read it from .ocel/deploy-result.json
aws s3api get-object --bucket <asset bucket> --key "$ISR_PREFIX/origin.json" /dev/stdout | jq .
# { "v": 1, "functionUrls": { "<routeId>": "https://<id>.lambda-url.<region>.on.aws/" } }
```

## 6. §9 acceptance item (a) — dedup and drain

The R2 key for a route entry is `<isrPrefix>/cache/<cacheKey>.cache.json`, where
`cacheKey` is the route path with the leading `/` stripped (`/` and `""` become `index`),
in bucket `ocel-edge-cache` (`frameworks/next/cache/src/index.mts`, `entryObjectKey`).

```bash
KEY="$ISR_PREFIX/cache/isr.cache.json"
r2_get() {
  curl -s -H "Authorization: Bearer $CLOUDFLARE_API_TOKEN" \
    "https://api.cloudflare.com/client/v4/accounts/$CLOUDFLARE_ACCOUNT_ID/r2/buckets/ocel-edge-cache/objects/$1"
}
```

1. **Warm the route**, then wait past its `revalidate` so the entry is stale. Record
   `lastModified` from the R2 body before driving anything.
2. **Purge the queue first** so the counts below are attributable:
   `aws sqs purge-queue --queue-url "$QUEUE_URL"` (SQS allows one purge per 60s).
3. **Drive exactly one edge request** at the stale route.
4. **Assert exactly ONE queue message.** The consumer drains fast, so read the send side
   rather than racing the queue depth: `NumberOfMessagesSent` on the queue over the
   minute, cross-checked against the consumer's own per-record log lines.

   ```bash
   aws cloudwatch get-metric-statistics --namespace AWS/SQS \
     --metric-name NumberOfMessagesSent \
     --dimensions Name=QueueName,Value=ocel-revalidate.fifo \
     --start-time <t0> --end-time <t1> --period 60 --statistics Sum
   ```

   `ApproximateNumberOfMessagesVisible` reading 0 proves nothing on its own — an empty
   queue is also the healthy drained state. **A detector that cannot fail is labelled as
   such, not cited as validation.**
5. **Drive from two distinct clients** (two colos if reachable; at minimum two distinct
   client IPs/connections) against the same stale generation and assert the count is
   **still one**. The dedup id is `sha256("<isrPrefix>:<routePath>:<lastModified>")`, so
   two colos seeing the same stale entry derive the same id and SQS collapses them —
   which also means **a second request after `lastModified` has moved is correctly a
   second message**, not a dedup failure. Order the two drives inside one generation.
6. **Consumer renders.** In the revalidator's log group, one record processed, outcome
   `ok`, no `origin-unusable`/`origin-unavailable`. Expect `RevalidateExpectMiss` only if
   the route went dynamic; it is a success by design.
7. **R2 `lastModified` advances.** Read the object again through the API and compare the
   field against the value recorded in step 1.
8. **The next edge admission refills with ZERO renders.** Drive one more request; assert
   the Lambda log group shows no new invocation for that route and the served entry is the
   new generation (`x-ocel-cache` of `HIT`/`PRERENDER`/`STALE`, never `MISS`).

**A 204 from `platform/edge/cloudflare/workers/isr-writer` is NOT proof the write landed.** It returns 204 for the
`"absent"` outcome too, so it cannot distinguish "landed" from "there was no snapshot to
land in". Every R2 assertion above must be a direct read through the Cloudflare API and a
byte comparison — this is the trap that `.15` and `ocelhq-yo9b` both call out.

## 7. §9 acceptance item (b) — the golden byte-comparison

This is the **authoritative** run of the gate `.26` landed; `.26`'s local run does not
prove the Lambda's behavior.

```bash
node scripts/e2e-next/assert-suppression-golden.mjs "$DEPLOYMENT_URL"
```

The script **hard-fails if neither leg of a pair reports `x-nextjs-cache: STALE`**, so a
pass means the suppression branch was really evaluated rather than short-circuited on a
fresh entry. Record which pairs reported STALE, not just the exit code.

## 8. §9 acceptance item (c) — the poison path

**The old "bad host" formulation is superseded** (amendment D): the message names no host,
so no message can name one. Drive an unresolvable message instead — either is valid:

- a `routeId` this build never recorded, or
- an `isrPrefix` pointing at a retired build whose `origin.json` is gone.

Send it with an explicit group and dedup id (a FIFO send without both is a runtime error):

```bash
aws sqs send-message --queue-url "$QUEUE_URL" \
  --message-group-id "poison" --message-deduplication-id "poison-$(date +%s)" \
  --message-body '{"v":1,"headers":{},"expect":null,"isrPrefix":"<retired prefix>","routeId":"never-recorded","routePath":"/isr","lastModified":0,"enqueuedAt":0}'
```

Assert:

- each receive logs `origin-unusable` and **fetches no origin** (no corresponding Function
  URL invocation in the app's Lambda log group);
- the message reaches `ocel-revalidate-dlq.fifo` after **5** receives
  (`maxReceiveCount: 5`);
- the DLQ's depth rises (there is no alarm to watch; read the queue directly):
  ```bash
  aws sqs get-queue-attributes --queue-url "$DLQ_URL" \
    --attribute-names ApproximateNumberOfMessages
  ```
- **no log line anywhere contains the record, its headers, or an error's text.** The
  record carries the app's `bypassToken` (amendment D, `.23`'s no-log-the-raw-record
  rule). Grep the log group for a fragment of the bypass token and require zero hits.

### The expected first-rollout DLQ traffic, and how to tell it apart

**The DLQ fills on the first rollout regardless of the poison test.** Every build already live when `.24` landed has no `origin.json`; each of its
enqueued routes fails `origin-unusable` through five receives and reaches the DLQ. It
clears once the 300s `MessageRetentionPeriod` drains and every live build has been
redeployed.

Distinguish them by reading the DLQ's messages:

```bash
aws sqs receive-message --queue-url "$DLQ_URL" --max-number-of-messages 10 \
  --visibility-timeout 0 --message-attribute-names All --attribute-names All
```

- **Rollout noise**: bodies whose `isrPrefix` names a build you did not deploy in this
  session, and the DLQ depth stops growing and drains within ~5 minutes of the last
  pre-existing build being redeployed.
- **Real failure**: a body whose `isrPrefix` is **this session's** `$ISR_PREFIX` and whose
  `routeId` this deploy did record — that is the consumer failing on a route that should
  have resolved, and it is a defect.
- A DLQ that is still growing after every live build has been redeployed and 300s has
  passed is a real failure whatever the bodies say.

## 9. §9 acceptance item (d) — the absorbed `.10` criteria, from THIS run

All of these must come from the same session and the same seeded route; running them
separately means deploying the substrate twice.

1. A regenerated ISR entry appears in the cache bucket under `<isrPrefix>/cache/*.cache.json`
   with the isr-writer in the path (assert the writer's log/metrics show the write, and
   read the object back through the R2 API).
2. The account-level isr-writer script uploads **with its DO migration tag** on this
   bootstrap.
3. The R2 native binding resolves to the substrate's real cache bucket (read the script's
   bindings through the API, as in §4).
4. A deployed Lambda's entry write authenticates against the seeded hash and lands at the
   key the edge reads back — same key, both directions.
5. `retireISRWriter` runs against a real prune (this is exercised by the teardown in §11).
6. **Resolve `.10`'s open question**: does the script-settings endpoint report a migration
   tag for a script migrated with the older single-tag form? `.15` established that **no
   migration tag is reported at all** — `settings.Migrations` comes back fully zeroed for a
   script that demonstrably carries its class — and the fix keys on the deployed classes
   instead (`platform/edge/cloudflare/deploy/durableobjectmigration.go`). Re-confirm on this run by
   calling the settings endpoint directly and recording the verbatim response; do not
   re-derive the API shape from docs.

## 10. The four review items from the bd issue

Each of these is a recorded divergence the live run exists to resolve. Report an answer
for every one, including "no observable difference", which is a result.

1. **Exercise a HEADER-VARYING route.** The queue leg sends four literal headers plus
   `x-forwarded-host`/`x-forwarded-proto`; `originBlocking` sends `allowHeader`-filtered
   client headers plus middleware overrides, and never sends the `x-forwarded-*` pair. A
   route that varies on an allowed header, or whose absolute URLs depend on the host, can
   render to **different bytes depending on which leg regenerated it**. Regenerate the
   §5 header-varying route once via the queue and once via `originBlocking` (force the
   fallback by temporarily unbinding the queue URL, or by driving the leg that refuses),
   and byte-compare the two R2 entries.
2. **Grep the edge logs for the two warn lines BEFORE concluding the queue is idle.** An
   empty queue is the healthy state, so a queue refusing every send looks identical to a
   queue nobody is filling. The lines are, verbatim:
   - `ocel: the revalidation queue refused the message with <status> — rendering through the origin instead`
   - `ocel: could not send to the revalidation queue`

   Use `wrangler tail` or the Cloudflare logs API on the worker script for the drive
   window. **Zero queue messages plus zero of these lines means the sender was never
   constructed** (missing env var, or `sqsRegion` rejecting the URL shape — see §4), which
   is a different failure from a refused send.
3. **Confirm the query-string divergence.** `originUrl` in `platform/edge/cloudflare/workers/entry/src/index.ts`
   builds `pathname + url.search`; the consumer composes `origin + routePath` alone. So
   the queue leg triggers without the query and the fallback leg with it. Drive the §5
   query-string route as `/q?x=1` on both legs and record whether the regenerated entry
   differs. Dropping the query is almost certainly *more* correct — the ISR entry is keyed
   on `routePath` alone and `refreshKey` is `${buildId}:${routePath}` — but it is an
   undocumented divergence and this run is what says whether a real route notices. Write
   the answer into the design doc either way.
4. **Exercise a STORE-LESS config** (`OCEL_CACHE_STORE` unbound). `.26`'s review found the
   two suppression halves compose into a **permanently frozen route** there: with no ISR
   store there is no interception, so no tier can observe the entry's staleness, and the
   `purpose: prefetch` stamp asks the Lambda not to render either. The fix gates the stamp
   on a named `admissionTier` (`platform/edge/cloudflare/workers/entry/src/index.ts`, `87d620e`). Deploy one app
   onto a substrate whose Cloudflare upload has no cache-store binding, make a route
   stale, and assert it **still regenerates** — the stamp must be absent on that forward.
   A pages-router `_next/data` request takes the same path on a fully bound worker, so
   drive one of those too.

## 11. Teardown — and verify it through the API

Anything stood up solely for this run is torn down, and the teardown is **verified through
the API**, the way `.16`'s and `.9`'s probe deploys were. A teardown asserted from an exit
code is not a teardown.

```bash
ocel preview rm --name <slug> --yes     # from a dir whose ocel.config.ts declares that slug

# Verify, do not assume:
aws cloudformation describe-stacks --stack-name <app stack>          # expect does-not-exist
aws lambda list-functions --query 'Functions[?contains(FunctionName, `<slug>`)].FunctionName'
aws s3api list-objects-v2 --bucket <asset bucket> --prefix "$ISR_PREFIX"   # expect KeyCount 0
curl -s -H "Authorization: Bearer $CLOUDFLARE_API_TOKEN" \
  "https://api.cloudflare.com/client/v4/accounts/$CLOUDFLARE_ACCOUNT_ID/workers/scripts" \
  | jq -r '.result[].id' | grep <slug>                               # expect no match
r2_get "$ISR_PREFIX/cache/isr.cache.json"                            # expect a miss
```

**What is NOT torn down**, deliberately: the account-level substrate (the revalidator
Lambda, its queues, `ocel-isr-writer*`). Those are the thing being proven, not
scaffolding for the proof. `ocelhq-wvag.11` already records that destroy leaves per-build
writer DO instances behind — check for and record any left by this run rather than
treating an absence of cleanup as a new finding.

Drain the DLQ once the poison test is recorded, or the next session inherits its
messages and reads them as its own failures:

```bash
aws sqs purge-queue --queue-url "$DLQ_URL"
```

## 12. Reporting discipline

Same discipline as `docs/research/cloudflare-cache-api-spike.md`:

- Report **distributions**, not single samples, for anything timing-dependent.
- **Count every discarded sample** and say why it was discarded.
- Label any detector that **cannot fail** as such, rather than citing it as validation.
  `ApproximateNumberOfMessagesVisible == 0` and a 204 from the isr-writer are both in this
  class.
- Record the answers to §10's four items even when the answer is "no observable
  difference" — that is the result the design doc needs.
