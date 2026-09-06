# Metering cost per route on Lambda and containers

Retrieved 2026-09-06. Every price is us-east-1 and carries its source. Where the rendered
AWS pricing page and the Price List Bulk API disagree, the Price List API wins and the
disagreement is named.

## Headlines

1. On ocel's Lambda topology the per-route metering question is largely already answered: `registerFunction` creates **one Lambda function per route** (`platform/aws/provider/deploy/function.go:498`), tagged `ocel:route`, so route cost *is* resource cost and falls out of free per-function CloudWatch metrics plus CUR — no meter, no agent, no per-request write.
2. The residual case is the **entry/router function**, which serves many routes in one function. That is the only place on Lambda where a runtime meter earns its keep.
3. A reference workload — 1M requests/month, 50 routes, 512 MB, 100 ms — costs **$1.03/month** in Lambda charges ($0.83 duration + $0.20 requests). Any telemetry path costing more than ~$0.10/month has failed the ticket's test.
4. CloudWatch custom metrics fail it by two orders of magnitude: `{Route}` × 3 metrics = 150 metrics = **$45.00/month**, 43× the workload. `{Route, StatusClass}` = **$225.00**. A `requestId` dimension = **$64,500**. Cost tracks dimension cardinality, not traffic ([CloudWatch pricing](https://aws.amazon.com/cloudwatch/pricing/)).
5. **Route must never become a CloudWatch dimension.** EMF does not help — it avoids the API call, not the metric-month charge ([EMF docs](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/CloudWatch_Embedded_Metric_Format.html)).
6. The membrane **cannot** learn billed duration in-band. `billedDurationMs` exists only in the `REPORT` line and Telemetry API `platform.report`, and AWS states telemetry for invoke *N* may arrive during *N+1* or at SHUTDOWN ([Telemetry API blog](https://aws.amazon.com/blogs/compute/introducing-the-aws-lambda-telemetry-api/)). Since Aug 1 2025 INIT is billed on all configurations ([compute blog](https://aws.amazon.com/blogs/compute/aws-lambda-standardizes-billing-for-init-phase/)), so `performance.now()` around the handler is strictly less than what is billed.
7. Therefore membrane-measured `durationMs` is a **proportion, not a measurement** — it belongs on the `estimated` rung of the confidence ladder, never `metered`.
8. Cheapest viable collection path: **aggregate per route in the Go bootstrap and emit one line per flush to stdout**. Per-request overhead is ~0 ms and $0 — the `request-end` control message already exists; only a `route` field is added. Rollups via Logs Insights at $0.005/GB scanned; 0.25 GB/month sits inside the CloudWatch free tier.
9. Containers are a different problem in kind. Fargate bills **task size × wall clock**, per second with a 1-minute minimum ([Fargate pricing](https://aws.amazon.com/fargate/pricing/)) — no per-request meter exists, so per-route cost is an *allocation* of a fixed cost, and the splitting ratio is a modelling choice.
10. Recommended cardinality cap: **bounded by the build-time route manifest, not a heuristic** — `RoutingManifest.pathnames` already enumerates the closed set. Cap at 500 templates per app with an OTel-style `overflow` bucket so the splits still sum to the bill.

---

## What the wrapper can know

`wrapWithOcelContext` in `platform/aws/membrane/src/shared/membrane.mts` already times every
request with `performance.now()` and emits `request-end {requestId, status, durationMs}` over a
unix socket to the Go bootstrap, which decodes it in `drainControl`
(`platform/aws/provider/cmd/membrane/bootstrap/nodechild.go:487` — currently an empty case).
The wrapper therefore already owns three of the four dimensions a cost fact needs.

**Route.** The framing in the ticket brief — that the route is baked into the function's env —
is not what the code does. `registerFunction` puts the route in the Lambda **tag** `ocel:route`
and in the function Description; the env carries `OCEL_APP`, `OCEL_DEPLOYMENT_ID` and `OCEL_SLUG`
(`platform/aws/provider/deploy/routerhost.go`), set from the deploy plan in
`appprogram.go:226`. So a per-route function knows its app and deployment from env, and its route
only via the tag, which the runtime cannot read without an API call.

That matters less than it sounds, because of the topology. One function per route means the
route dimension is carried by the function's own identity: `AWS/Lambda` metrics are already
per-function, CUR rows are already per-function ARN, and the `ocel:route` tag is already on the
resource for tag-based attribution. Nothing needs to be measured at request time.

The **entry function** is the exception: it hosts the router and dispatches many routes.
There the matched template is available in-process as `result.resolvedPathname`, and is already
written onto the response as `x-matched-path` (`frameworks/next/router/src/index.mts:609`). The
membrane's `finalize` runs on `res.finish`, after headers are set, so `res.getHeader("x-matched-path")`
yields `http.route` with no new plumbing and no new I/O.

**Deploy id and app** are env vars already present. **Memory size** is available as the standard
`AWS_LAMBDA_FUNCTION_MEMORY_SIZE`, already read elsewhere in the tree
(`platform/aws/membrane/src/next/use-cache-default.mts:23`).

**Billed duration is the one thing it cannot have.** The `REPORT` line carries `Billed Duration`,
`Memory Size`, `Max Memory Used`, `Init Duration`, and — in the newer format — `Status` and
`Error Type` ([REPORT fields](https://docs.aws.amazon.com/lambda/latest/dg/nodejs-logging.html)).
The Telemetry API's `platform.report` carries `ReportMetrics {billedDurationMs, durationMs,
initDurationMs?, maxMemoryUsedMB, memorySizeMB, restoreDurationMs?}`
([schema reference](https://docs.aws.amazon.com/lambda/latest/dg/telemetry-schema-reference.html)).
Both are out-of-band. AWS's own sample extension states that "new telemetry might arrive either
before or after dispatching existing telemetry" and that the last invocation's events are
dispatched at shutdown
([aws-lambda-extensions](https://github.com/aws-samples/aws-lambda-extensions/tree/main/go-example-telemetry-api-extension)).

Two further gaps make `performance.now()` a lower bound rather than an estimate of the bill:

- **INIT is now billed everywhere.** "Effective August 1, 2025, INIT phase will be billed across
  all configuration types" — the change moved on-demand + ZIP + managed-runtime functions from
  unbilled to billed INIT ([compute blog](https://aws.amazon.com/blogs/compute/aws-lambda-standardizes-billing-for-init-phase/)).
  The wrapper's timer starts after INIT.
- **Suppressed inits are invisible.** "Lambda doesn't explicitly report an additional INIT phase
  in CloudWatch Logs. Instead, you might notice that the duration in the REPORT line includes an
  additional INIT duration + the INVOKE duration"
  ([runtime environment](https://docs.aws.amazon.com/lambda/latest/dg/lambda-runtime-environment.html)).

### Could an internal extension close the gap?

ocel already ships a layer mounted at `/opt/ocel` and sets `AWS_LAMBDA_EXEC_WRAPPER=/opt/ocel/bootstrap`
(`function.go:34`, `:471`), so adding `/opt/extensions/ocel-meter` is a payload change, not new
deployment machinery. But it should not be done.

Whether an *internal* extension may subscribe to the Telemetry API is **unverified** — AWS states
no prohibition, but every narrative and diagram assumes an external one. What is documented is
disqualifying either way: "Internal extensions are started and stopped by the runtime process, so
they are not permitted to register for the `Shutdown` event"
([Extensions API](https://docs.aws.amazon.com/lambda/latest/dg/runtimes-extensions-api.html)) —
forfeiting exactly the final flush the reference implementation depends on.

An external extension works but is self-defeating at this workload. "Each extension must complete
its initialization before Lambda invokes the function", "You are charged for the execution time
that the extension consumes (in 1 ms increments)", and "there is no independent post-invoke
phase" — so a post-response flush is billed duration
([extensions](https://docs.aws.amazon.com/lambda/latest/dg/lambda-extensions.html)). Post-Aug-2025
that init cost lands inside billed `Init Duration` on every cold start. Against $1.03/month of
workload, a few ms per cold start plus a per-invocation flush is a material fraction of the bill.
The apparatus taxes the thing it measures.

Lambda Insights is the packaged version of the same trade and prices out badly: 10 metric names ×
2 dimension sets = 20 metric-months ≈ **$6.00 per function version per month**
([Lambda Insights](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/Lambda-Insights.html)) —
6× the entire workload, per function, before log ingestion. Its per-invocation byte volume is
**unverified**; AWS publishes no figure.

---

## Cost of every collection path

Reference workload: 1M requests/month, 50 routes, ~250-byte records, us-east-1.

| Path | $/month | Note |
|---|---|---|
| Lambda built-in metrics (`Invocations`, `Duration`) | **$0.00** | free; per-function = per-route on ocel's topology |
| `GetMetricData`, 150 metrics daily | $0.045 | reading free metrics is not free — $0.01/1,000 metrics |
| Bootstrap aggregation → stdout → Logs Standard | **$0.13** | free-tier covered; $0.50/GB ingest |
| Logs Infrequent Access | $0.07 | but no EMF, no metric filters, no subscription filters, immutable class |
| Firehose → Iceberg Tables | $0.011 | no 5 KB roundup |
| Firehose Direct PUT → S3 | $0.14 | bills 5 GB for 0.25 GB |
| S3 batched, 1 object/hour | $0.01 | 720 PUTs |
| S3 batched, 1 object/minute | $0.22 | 43,200 PUTs |
| DynamoDB per-route-per-hour counters | $0.023 | 36,000 WRU |
| DynamoDB, one write per request | $0.625 | $0.625/M WRU |
| DynamoDB per-route-per-minute counters | $1.35 | *more* than per-request |
| Application Signals | $1.50 | $1.50/M signals, first 100M |
| S3 direct PUT per request (Standard) | $5.01 | $0.005/1,000 PUTs |
| S3 direct PUT per request (One Zone-IA) | $11.22 | 128 KB minimum billable object |
| **CloudWatch metrics `{Route}` × 3** | **$45.00** | 43× the workload |
| **CloudWatch metrics `{Route, StatusClass}` × 3** | **$225.00** | 218× the workload |
| CloudWatch metrics + `requestId` | $64,500 | the trap, stated by AWS |

Sources: [CloudWatch pricing](https://aws.amazon.com/cloudwatch/pricing/),
[Firehose pricing](https://aws.amazon.com/firehose/pricing/),
[S3 storage classes](https://docs.aws.amazon.com/AmazonS3/latest/userguide/storage-class-intro.html),
[DynamoDB on-demand](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/on-demand-capacity-mode.html),
Price List Bulk API for `AmazonCloudWatch`, `AmazonS3`, `AmazonDynamoDB`, `AWSLambda`
(publication 2026-08-31).

Four traps worth naming, because each is a "cheap" option that is not:

- **S3 One Zone-IA costs 2.2× S3 Standard** for 250-byte records: "If an object is less than
  128 KB, Amazon S3 charges you for 128 KB."
- **Firehose bills 20× the real bytes.** "Ingestion pricing is tiered and billed per GB ingested
  in 5KB increments", and the FAQ adds that "the 5KB roundup is calculated at the record level
  rather than the API operation level" — batching does not help.
- **Logs Infrequent Access forecloses EMF and subscription filters**, and "after a log group is
  created, its log class can't be changed"
  ([log classes](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/CloudWatch_Logs_Log_Classes.html)).
- **Minute-granularity aggregation costs more than per-request writes** at this volume: 50 routes
  × 43,200 minutes = 2.16M buckets exceeds 1M requests. Aggregate on the hour, not the minute.

Two AWS documentation defects surfaced and should be coded around rather than trusted:
`PutMetricData` is priced "$0.01/M requests" on the rendered page but `$0.01 per 1,000 requests`
in the Price List API (1000× apart; the API is the billing truth); and the Telemetry API
`maxItems` default is documented as both 1,000 and 10,000 on different pages — set it explicitly.

---

## Containers

The container side is not a metering problem. Fargate pricing is stated as a formula over
configuration and wall clock — "(# of Tasks) x (# vCPUs) x (price per CPU-second) x (CPU duration
per day by second)" — "calculated per second with a 1-minute minimum", measured "from the time you
start to download your container image (Docker pull) until the task terminates"
([Fargate pricing](https://aws.amazon.com/fargate/pricing/)). us-east-1 Linux/x86: $0.04048/vCPU-hr
and $0.004446/GB-hr; ARM is ~20% lower at $0.03238 and $0.003560.

Nothing in that formula references requests. The bill is fixed the moment a task size and lifetime
are chosen, so any per-route number must be a split of a constant, and the choice of splitting
ratio — CPU-seconds, request duration, request count — is a modelling decision that cannot be
validated against a meter. This is the structural opposite of Lambda, where GB-seconds are metered
per invocation and route costs are additive and exact.

The good news is that the billable quantity is readable from inside the container with no API call
and no credentials. The ECS task metadata endpoint v4 `/task` document returns task-level
`Limits.CPU` (in vCPUs) and `Limits.Memory` (MiB), plus `Cluster`, `TaskARN`, `Family`, `Revision`,
`ServiceName`, `LaunchType`, `AvailabilityZone`, `PullStartedAt`/`PullStoppedAt` and
`EphemeralStorageMetrics`
([field reference](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/task-metadata-endpoint-v4-fargate-response.html)).
`Limits.CPU × vCPU-rate + Limits.Memory × GB-rate` reconstructs the bill locally. Container-level
`Limits.CPU` values are CPU *shares*, not billable vCPUs — and on Fargate "the CPU quota and period
values are not used for CPU limiting", so cgroups are not a substitute. `/task/stats` supplies the
per-container CPU and memory deltas that would drive a split.

Container Insights with enhanced observability prices at **$0.21 per 1M observations**, with
documented emission rates of 1,720/min per cluster and 138/min per pod
([CloudWatch pricing](https://aws.amazon.com/cloudwatch/pricing/)) — roughly $16/node/month plus
$16/cluster/month. Note the ECS developer guide still carries a contradicting "charged as custom
metrics" box; treat the pricing page as authoritative and the non-enhanced ECS model as
**unverified**.

For attribution in front of a container, ALB access logs are the obvious source and the wrong one:
the route is absent (only the raw `request_line` and an integer `matched_rule_priority`), files
land per node per 5 minutes, and AWS states logs are written "on a best-effort basis... not as a
complete accounting of all requests"
([access logs](https://docs.aws.amazon.com/elasticloadbalancing/latest/application/load-balancer-access-logs.html)).
Envoy's `ROUTE_NAME` plus `DURATION` is the only proxy-side source that carries a route template
natively ([substitution formatter](https://www.envoyproxy.io/docs/envoy/latest/configuration/advanced/substitution_formatter)).
An in-process meter — the container analogue of the membrane — remains the better source.

---

## Dimension vocabulary and the cardinality cap

The vocabulary is settled prior art. OpenTelemetry's `http.route` is Stable and defined as "the
matched route template for the request. This MUST be low-cardinality and include all static path
segments, with dynamic path segments represented with placeholders", with the stronger rule that
it "MUST NOT be populated when this is not supported by the HTTP server framework as the route
attribute should have low-cardinality and the URI path can NOT substitute it"
([HTTP spans](https://opentelemetry.io/docs/specs/semconv/http/http-spans/)). The metric
`http.server.request.duration` takes `{http.request.method, url.scheme}` as required and
`{http.route, http.response.status_code, error.type}` as conditionally required — and notably
`url.path` appears at no level ([HTTP metrics](https://opentelemetry.io/docs/specs/semconv/http/http-metrics/)).
`http.request.method` collapses unknown methods to the literal `_OTHER`, capping that dimension at
about ten values. ocel should adopt this set verbatim.

On the cap itself, three enforcement postures exist and only one is right for cost. AWS **bills**
for cardinality — unbounded and expensive; Powertools states the rule as
`unique metric = (metric_name + dimension_name + dimension_value)` and routes high-cardinality data
to `add_metadata` instead ([Powertools Metrics](https://docs.aws.amazon.com/powertools/python/latest/core/metrics/)).
Prometheus **advises** a number — "keep the cardinality of your metrics below 10", investigate
above 100 ([instrumentation practices](https://prometheus.io/docs/practices/instrumentation/)).
OpenTelemetry **truncates**: a default `aggregation_cardinality_limit` of 2000, with overflow
aggregated into a synthetic `otel.metric.overflow=true` bucket rather than dropped
([Metrics SDK](https://opentelemetry.io/docs/specs/otel/metrics/sdk/)).

The OTel posture is the only one that keeps the total correct while capping cardinality, which is
exactly the property a cost system needs: the per-route splits must sum to the actual bill.

ocel is better placed than any of them, because it does not need a heuristic. `RoutingManifest`
carries `pathnames: string[]` and `dispatch: Record<string, DispatchTarget>`
(`frameworks/next/protocol/src/routing-manifest.mts`) — the closed set of route templates, known at
build time and shipped into the function. Cardinality is bounded by the manifest, and any value not
in it is by definition a bug or an attack, not a route.

**Recommendation: cap at 500 route templates per app, enforced against the manifest, with
everything else folded into a single `__overflow__` bucket.** 500 is far above any real Next app
and costs nothing on the log path; the cap exists to bound a compromised or buggy producer, not to
ration legitimate routes. User-defined dimensions get a separate, much tighter budget — at most
three keys with a declared, finite value set — since their cardinality is not manifest-bounded.

Worth recording as market context: no FinOps product does this. Vantage's Kubernetes agent stops
at cluster/namespace/service/label with an `__idle__` namespace ([docs](https://docs.vantage.sh/kubernetes/));
Datadog's Container Cost Allocation stops at pod level and splits host cost by a fixed
"60% for the CPU and 40% for the memory" ratio
([docs](https://docs.datadoghq.com/cloud_cost_management/container_cost_allocation/)). Both offer
"cost per X" only as a scalar division at report granularity. Per-route cost attribution is either
the gap worth filling or evidence that the split is too arbitrary to sell.

---

## Recommendation

**Cheapest viable collection path.** Do nothing per request that costs anything.

1. **Per-route functions**: derive route cost from free per-function `AWS/Lambda` metrics and from
   CUR joined on the `ocel:route` tag. No meter. Collection cost $0; reading costs $0.045/month
   via daily `GetMetricData`.
2. **Entry/router function**: add `route` to the existing `request-end` payload, read from
   `res.getHeader("x-matched-path")`; aggregate per `{route, method, status_class}` in the Go
   bootstrap's `drainControl`; flush one aggregated line to stdout per N invocations and at
   shutdown. Rollups via Logs Insights.
3. Never publish route as a CloudWatch metric dimension, via `PutMetricData` or EMF.

**Per-request overhead: ~0 ms and $0.** The timer, the socket write and the control message all
exist today; the change is one header read and an in-memory counter increment. No new syscall, no
new network call, no extension, no cold-start tax. The only new cost is the aggregated log line —
0.25 GB/month, inside the CloudWatch Logs free tier, $0.13 beyond it.

**Confidence ladder placement.** Membrane-derived numbers are `estimated`, never `metered`: they
measure handler wall time, which excludes billed INIT and runtime overhead. `metered` is reserved
for `billedDurationMs` from the `REPORT` line, which arrives via Logs 5–10 minutes later and can
be joined on `requestId` to reconcile the estimate. `billed` remains CUR.

---

## Open questions

- **Can an internal extension subscribe to the Telemetry API?** No AWS statement either way. Only
  resolvable empirically, and the answer changes nothing given the `Shutdown` prohibition — but it
  bounds the design space if a future need appears.
- **What is Lambda Insights' per-invocation byte volume?** AWS publishes no figure, so its log
  ingestion cost cannot be estimated without measurement.
- **Which Firehose destination triggers the `DirectPUT-no-rounding-BilledBytes` SKU** at $0.08/GB?
  The SKU exists in the Price List API; the pricing page does not name its conditions.
- **Does the non-enhanced Container Insights model still bill as custom metrics on ECS?** The ECS
  developer guide and the CloudWatch pricing page contradict each other.
- **What splitting ratio should containers use** — CPU-seconds, request duration, or request
  count? This is a taste call with no meter to validate it, and it should be named as such in the
  scope-model decision rather than settled here.
- **Does the ISR/cache writer path need its own route dimension?** ISR revalidation is work
  attributable to a route but not to a request, and the reference workload does not cover it.
- **Is there an equivalent of `x-matched-path` for non-Next frameworks?** The recommendation above
  leans on a Next-specific header; the host-neutral serving runtime needs a port for this.
