# Prior art: how FinOps tools attribute, dimension, and alert

Research for #1011 (part of #1010). All facts retrieved **2026-09-06** against primary sources
(vendor docs, API references, pricing pages, repository source and LICENSE files). Claims that
could not be confirmed from a primary source are marked *unverified*.

## Headlines

1. **Every tool attributes through the bill, not through the workload.** Tags plus a rules engine over billing-line fields (account, service, region, usage type, resource id) is the universal mechanism; Vantage Virtual Tags, CloudZero CostFormation, Cloudability Business Mappings, Harness Cost Categories and Datadog Tag Pipelines are the same idea with different syntax.
2. **User-defined dimensions are always rule-derived, never instrumented.** A "custom dimension" is a saved filter expression evaluated against data already in the bill. The only path for outside data is a separate, coarse *push* channel (CloudZero Telemetry API, Vantage Business Metrics, Datadog "dynamic by metric").
3. **Nobody computes cost per HTTP route or per request.** Every "cost per API call" feature is a division: an already-attributed cost slice divided by a customer-supplied denominator, at daily granularity. The numerator never gets finer than hourly-per-resource.
4. **The finest billing granularity that exists anywhere is hourly per resource**, and AWS CUR explicitly blanks `lineItem/ResourceId` for "data transfers and API requests" — the exact class of spend that caused the map's fluke day.
5. **Detection latency is floored by the billing pipeline, not the algorithm.** AWS Cost Anomaly Detection: "it can take up to 24 hours to detect an anomaly after a usage occurs." Datadog cost monitors run every 30 minutes but on a **48-hour delayed window**. Cloudability re-evaluates every 24 hours. No billing-derived detector beats ~24h.
6. **Minimum-spend floors would have silently swallowed the fluke day.** Datadog will not flag a daily cost below **$5**; Vantage requires **$5 absolute AND 0.5% of report total**; CloudZero's automatic threshold scales off 30-day spend. The map's $1.74 day is below every published floor.
7. **Algorithms are mostly undisclosed.** Harness names **Prophet** on 42 days of history; Datadog names its **`agile`** anomaly algorithm with two bounds and monthly seasonality; AWS says only "machine learning models" — but as of **Nov 2025** AWS switched to **rolling 24-hour windows comparing current cost against equivalent periods from previous days**, which is materially the map's velocity guard.
8. **Allocation of shared cost has exactly three shapes everywhere**: even split, proportional-to-spend, and weighted-by-an-external-metric. OpenCost's spec names all three; Datadog, CloudZero and Vantage each ship the same trio.
9. **The reusable open source is the allocation engine and the price book, not the product.** OpenCost (Apache-2.0, CNCF Incubating) and Cloud Custodian (Apache-2.0, CNCF Incubating) are genuinely reusable; the AWS Price List Bulk/Query API is free; `ec2instances.info` is MIT. Infracost's CLI is Apache-2.0 but its **`cloud-pricing-api` repo now 404s** — the price book has closed.
10. **Contradicting the map: a linked account can create its own CUR 2.0 / FOCUS Data Export of its own data.** The payer-side blocker applies to cost *allocation tags* and CloudWatch billing metrics, not to Data Exports — so the hourly, resource-id-bearing fact table the sketch wants is reachable standalone today.

---

## Attribution: how a billing line becomes an app or a team

The mechanism is the same in every tool and it is subtractive, not additive: start from the
provider's billing export, then classify rows with rules.

**Vantage** ingests the CUR and, for per-resource costs, provisions a dedicated CUR with resource
IDs enabled into a bucket in the customer's account ([docs.vantage.sh/tagging](https://docs.vantage.sh/tagging)).
`lineItem/ResourceId` is only populated when that flag is on. Kubernetes is the exception: an
in-cluster agent polls `kube-apiserver` and the kubelet `/metrics/resource` endpoint (default 60s,
configurable 5–60s), prorates the instance price across CPU/RAM/GPU/storage to the minute, and
ships hourly aggregates ([docs.vantage.sh/kubernetes_agent](https://docs.vantage.sh/kubernetes_agent)).

**CloudZero** classifies with CostFormation (CFDL), a YAML DSL whose rules test a *Source* —
`Account`, `Region`, `Service`, `UsageType`, `Operation`, `Tag:<name>`, `K8s:Namespace`, or
`User:Defined:<id>` — against conditions including regex `Matches` and `ForDateRange`
([docs.cloudzero.com/docs/cfdl-reference](https://docs.cloudzero.com/docs/cfdl-reference)).
Its glossary states tags are optional: account/service/namespace metadata already in the bill suffices.

**Cloudability** calls these Business Mappings: ordered statements of `matchExpression` /
`valueExpression` over "vendor-supplied fields such as tags, account names, regions, and service
names" plus derived attributes, capped at 10 dimensions and 300,000 statements each
([ibm.com Cloudability Business Mapping](https://www.ibm.com/docs/en/cloudability-commercial/cloudability-premium/saas?topic=spend-cloudability-business-mapping)).

**Datadog** is the only one that runs a two-stage pipeline: Tag Pipelines first repair and infer
tags (retroactive ~3 months), then Custom Allocation Rules split shared cost onto those tags
(retroactive ~1 month) ([docs.datadoghq.com/cloud_cost_management/allocation/](https://docs.datadoghq.com/cloud_cost_management/allocation/)).
Its AWS setup *requires* a CUR with hourly granularity, resource IDs and split cost allocation
enabled, and warns 48–72 hours before data populates
([setup/aws](https://docs.datadoghq.com/cloud_cost_management/setup/aws/)).

**AWS itself** does the container half natively. Split Cost Allocation Data adds per-ECS-task and
per-EKS-pod rows to the CUR, "based on the amortized cost of the instance and the percentage of CPU
and memory resources consumed," and auto-creates `aws:eks:namespace`, `aws:eks:workload-name` and
friends as cost allocation tags
([split-cost-allocation-data](https://docs.aws.amazon.com/cur/latest/userguide/split-cost-allocation-data.html)).

The pod-cost formulas diverge in an instructive way. OpenCost normalizes node base prices so CPU +
RAM + GPU sum to the node cost, then charges `max(request, usage) × duration × rate`
([opencost spec](https://github.com/opencost/opencost/blob/develop/spec/opencost-specv01.md)).
Datadog instead uses a **fixed 60% CPU / 40% memory** split (95/3/2 with a GPU)
([container_cost_allocation](https://docs.datadoghq.com/cloud_cost_management/allocation/container_cost_allocation/)).
One is derived, one is a constant; both are presented to users as "the" pod cost.

## Custom dimensions: rules over the bill, plus a coarse push channel

No tool lets a running application declare a dimension. What every tool offers is a rules engine
plus, separately, an upload endpoint.

The rules half is Virtual Tags, CostFormation, Business Mappings, Cost Categories, Tag Pipelines —
all evaluated against fields already present. Vantage caps this at 50 virtual tag configs of 100
values each ([docs.vantage.sh/tagging](https://docs.vantage.sh/tagging)).

The push half is where external reality enters, and it is uniformly coarse:

- **Vantage Business Metrics** — CSV upload, `PUT /business_metrics/{token}/values.csv`, or a daily
  scheduled sync from CloudWatch/Datadog/Snowflake. **Minimum granularity: daily**
  ([docs.vantage.sh/per_unit_costs](https://docs.vantage.sh/per_unit_costs)).
- **CloudZero Telemetry Streams** — JSON API or CSV; two stream types, *unit cost metrics* and
  *allocation streams*, the latter letting you split shared cost with no pre-existing tag. Docs
  recommend "at least daily," and state ingest takes **up to 24 hours**
  ([telemetry-streams](https://docs.cloudzero.com/docs/telemetry-streams)). Limits: 5 MB/request,
  100 records/sec ([telemetry-api](https://docs.cloudzero.com/reference/telemetry-api-1)).
- **Datadog "Dynamic by metric"** — the split weight is any Datadog metric query, e.g. shared
  Postgres cost by total query execution time per team
  ([custom_allocation_rules](https://docs.datadoghq.com/cloud_cost_management/allocation/custom_allocation_rules/)).

Datadog's version is the most interesting near-miss: because the weight is an arbitrary metric, and
Datadog already holds request counts by endpoint, a user *could* hand-build route-level splitting.
No documentation describes doing so, and there is no automatic APM-to-cost join — the "cost per
service" language on the product page is tag-based allocation, not a correlation engine.

## Cost per HTTP route or per request: the universal gap

Every "cost per request" feature reduces to the same division, with the denominator supplied by the
customer. CloudZero's own docs are explicit that the denominator "is oftentimes found in an
observability tool or database query, and typically it is not a dataset found in your cloud
provider's bill" ([collecting-unit-cost-telemetry](https://docs.cloudzero.com/docs/collecting-unit-cost-telemetry)).
Vantage's Unit Costs work identically. Neither instruments HTTP.

The raw per-request signals *do* exist — they are simply never priced:

- **Lambda** emits a per-invocation `REPORT` line with `RequestId`, `Duration`, `Billed Duration`,
  `Memory Size`, `Max Memory Used` into CloudWatch Logs. Pricing ($0.0000166667/GB-second plus
  $0.20/1M requests, [lambda/pricing](https://aws.amazon.com/lambda/pricing/)) is applied to the
  monthly aggregate, never to the line.
- **API Gateway** bills per million requests; **ALB** does not bill per request at all, only per
  LCU-hour computed as the max across four dimensions
  ([elasticloadbalancing/pricing](https://aws.amazon.com/elasticloadbalancing/pricing/)).
- **Cloudflare Workers** analytics expose request count, CPU time and subrequests per Worker with
  **no dollar figure** at the metrics layer
  ([workers metrics-and-analytics](https://developers.cloudflare.com/workers/observability/metrics-and-analytics/)).
- **Vercel** meters Active CPU-seconds, GB-hours and Edge Middleware invocations "multiplied by
  every route" ([vercel.com/docs/pricing](https://vercel.com/docs/pricing)) — the closest anything
  comes to route-level, and still a usage meter, not a cost.
- **AWS Application Signals** gives per-operation RED metrics with no cost dimension whatsoever.

And the bill cannot close the gap: `lineItem/ResourceId` "is blank for usage types that aren't
associated with an instantiated host, such as data transfers and **API requests**"
([Lineitem-columns](https://docs.aws.amazon.com/cur/latest/userguide/Lineitem-columns.html)).
Request-priced spend is exactly the spend the CUR refuses to attribute — the same wall the map hit
with its shared state bucket.

No maintained OSS project was found that multiplies OpenTelemetry spans by a price to yield
per-request cost. The adjacent work is per-*team* attribution via span resource attributes, and
LLM token-cost tracing via the GenAI semantic conventions — neither generalises to HTTP routes
(*unverified* that none exists; none was found).

## Anomaly detection: algorithm, latency, floor, delivery

| Tool | Algorithm | Latency | Minimum to fire | Delivery |
|---|---|---|---|---|
| AWS CAD | "machine learning models"; since Nov 2025, rolling 24h windows vs equivalent prior periods | runs ~3x/day; **"up to 24 hours to detect an anomaly after a usage occurs"**; 10 days history per new service | user-set absolute $ and/or %; no published floor | SNS (to Slack/Chime), email, EventBridge, User Notifications |
| Datadog | **`agile`**, two bounds, monthly seasonality | 30-min evaluation on a **48h delayed window**; needs 1 month history | **daily cost at least $5** | email, Slack, PagerDuty |
| Vantage | ML forecast on up to 6 months daily history; flags actual above upper bound | on cost-report refresh; provider lag 24–48h | **$5 absolute AND 0.5% of report total** | email, Slack, Teams, Jira |
| CloudZero | undisclosed | monitors to hourly granularity; *unverified* spend-to-alert lag | automatic sliding scale off 30-day spend, or manual % of avg daily | Slack, email, Google Chat |
| Cloudability | "Cost Segment Formation" | **every 24 hours**, re-evaluating the past 7 days | absolute $ and/or `Unusual Spend / Expected Spend` % | email, PagerDuty |
| Harness | **Prophet**, 42 days history, confidence-interval corridor | *unverified* (doc URLs 404 at retrieval) | absolute $ and/or % | Slack, email |
| Kubecost | mean over lookback window plus outlier threshold | on ETL | configurable Minimum Cost | Slack, Teams, email, webhook, Alertmanager |

Two things follow. First, the *algorithm* is not the differentiator — AWS's post-Nov-2025 rolling
24-hour comparison is materially the map's proposed velocity guard, shipped and free. Second, the
*latency floor* is set by the billing pipeline: Cost Explorer refreshes "up to three times daily"
([ce-api-best-practices](https://docs.aws.amazon.com/cost-management/latest/userguide/ce-api-best-practices.html)),
AWS Budgets "up to three times a day… typically 8–12 hours after the previous update"
([budgets-managing-costs](https://docs.aws.amazon.com/cost-management/latest/userguide/budgets-managing-costs.html)),
and CUR 2.0's refresh cadence is **"Daily — export is refreshed up to one time per day"** even when
granularity is hourly
([dataexports-create-standard](https://docs.aws.amazon.com/cur/latest/userguide/dataexports-create-standard.html)).

For the map's fluke day this is decisive. A $1.74 day is below every published minimum, and 24
hours late. The only signals that would have caught it in minutes are *usage*-derived, not
billing-derived: CloudWatch S3 request metrics (opt-in, 1-minute, filterable by prefix via
`FilterId`, delivered best-effort — "the completeness and timeliness of metrics are not guaranteed",
[cloudwatch-monitoring](https://docs.aws.amazon.com/AmazonS3/latest/userguide/cloudwatch-monitoring.html)),
S3 server access logs (free but "might not be delivered at all", hours of latency), or CloudTrail
data events at **$0.10 per 100,000 events** ([cloudtrail/pricing](https://aws.amazon.com/cloudtrail/pricing/)).
DoiT is the only vendor found doing this commercially — estimating on-demand cost from CloudTrail
and Cloud Audit Logs at hourly, service-level granularity — and AWS publishes an **MIT-0** reference
architecture doing the same with CloudTrail into OpenSearch Random Cut Forest
([aws-samples/near-realtime-aws-usage-anomaly-detection](https://github.com/aws-samples/near-realtime-aws-usage-anomaly-detection)).

Note also that CloudWatch's own `EstimatedCharges` billing metric is not a substitute: it is
cumulative month-to-date, supports only static thresholds, and for a linked account is published
only if the **payer** enables "Receive Billing Alerts"
([monitor_estimated_charges_with_cloudwatch](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/monitor_estimated_charges_with_cloudwatch.html)).

## Licensing: what is actually open

Verified via the GitHub API on 2026-09-06:

| Project | License | State |
|---|---|---|
| OpenCost | Apache-2.0 | CNCF **Incubating** since 2024-10-25; active |
| Cloud Custodian | Apache-2.0 | CNCF **Incubating** since 2022-09-14; active |
| Infracost CLI | Apache-2.0 | active (12.5k stars) |
| `infracost/cloud-pricing-api` | was Apache-2.0 | **404 — repo no longer public** |
| Kubecost cost-analyzer chart | Apache-2.0 | active; Enterprise layer proprietary (IBM) |
| CloudZero agent / costformation / telemetry-library | Apache-2.0 | active |
| Vantage `ec2instances.info` | MIT | active, 5.8k stars |
| Vantage helm-charts | MIT | active — but the **agent binary itself is not published** |
| Komiser | **Elastic License 2.0** | not OSI; last push 2026-04-12; docs domain no longer resolves |
| Harness CCM | none | no open component found |

Komiser's licence changed from MIT to ELv2 in commit `813ec50b` (2022-06-07); ELv2 forbids offering
the software "as a hosted or managed service." Its per-resource cost is also an *estimate*: it
multiplies the AWS Pricing API on-demand rate by uptime, so it diverges under RIs, Savings Plans and
Spot.

Cloud Custodian is the enforcement precedent. It reads **resource state**, not billing — with two
narrow exceptions in source: a `cost-optimization` filter over AWS Cost Optimization Hub
recommendations (`c7n/filters/costhub.py`) and a `budget` resource wrapping `describe_budgets`
(`c7n/resources/budgets.py`); Azure alone has a real `CostFilter` querying Azure Cost Management.
Its actions (`stop`, `terminate`, `mark-for-op`, tag mutation) run in Lambda/EventBridge mode, and
`c7n-mailer` delivers to SES, Slack, Datadog or Splunk.

Infracost is a different category entirely: pre-deploy estimation from Terraform/CloudFormation/CDK,
covering "over 1,100 resources," with usage assumptions in `infracost-usage.yml`. It performs no
runtime attribution and no anomaly detection.

## The three patterns that recur

1. **Classify, don't instrument.** Attribution is a rules engine over billing fields. The workload
   is never asked what it is; the bill is interrogated after the fact.
2. **Shared cost splits three ways.** Even, proportional-to-spend, or weighted by an external
   metric — named identically in the OpenCost spec, CloudZero Allocation Dimensions, Datadog
   allocation rules and Vantage Virtual Tags.
3. **Unit cost is a division with a pushed denominator.** Cost per customer, per order, per API
   call — all the same endpoint shape, all at daily granularity, all the customer's problem to
   populate.

## The one thing nobody does

**Nobody prices a request.** Every platform emits a precise per-invocation signal and every platform
prices the monthly aggregate of it; not one first-party dashboard joins the two back down to "this
route cost $X." The join is left to the customer, and the CUR structurally cannot help because it
blanks resource IDs on exactly the request-priced line items. A tool that owns both the deploy and
the runtime — knowing which function backs which route, and holding the `Billed Duration` per
invocation — is the only party positioned to close it, and that is precisely ocel's ocel-managed
mode.

Second-order: nobody detects *below* the billing pipeline's latency at the account level with no
prior setup. DoiT comes closest and is closed; the AWS sample is MIT-0 but requires an OpenSearch
domain.

## Open-source pieces ocel could reuse rather than rebuild

- **AWS Price List Bulk / Query API** — free, IAM-gated, SKU-level, endpoints at
  `api.pricing.{us-east-1,eu-central-1,ap-south-1}.amazonaws.com`
  ([price-changes](https://docs.aws.amazon.com/awsaccountbilling/latest/aboutv2/price-changes.html)).
  This is the price book. Infracost's closing of `cloud-pricing-api` is the warning: do not depend
  on someone else's.
- **`ec2instances.info` (MIT)** — a maintained, normalized instance/price dataset already built on
  that API.
- **OpenCost (Apache-2.0)** — reuse the *allocation vocabulary* (max(request,usage), idle as a
  first-class line item, the three shared-cost distributions) even where Kubernetes is irrelevant.
  Upstream OpenCost does **no** billing reconciliation; that is Kubecost's commercial layer, which
  needs a 48-hour window before actuals are complete.
- **Cloud Custodian (Apache-2.0)** — a proven policy-to-action enforcement engine with a mature YAML
  DSL and multi-account fan-out (`c7n-org`), if `block`/`stop` tiers need a substrate.
- **FOCUS 1.4 (CC-BY-4.0)** and AWS's **FOCUS 1.0 / 1.2 with AWS columns** exports — a ready column
  vocabulary (`ResourceId`, `BilledCost`, `EffectiveCost`, `ChargeCategory`, `Tags`) for the fact
  schema, satisfying the map's "should not fight FOCUS."
- **`aws-samples/near-realtime-aws-usage-anomaly-detection` (MIT-0)** — CloudTrail into Random Cut
  Forest, as a reference for the pre-bill detector.

## Open questions

- **Does the fluke-day floor require CloudTrail data events?** S3 request metrics are opt-in per
  bucket and best-effort; CloudTrail data events cost $0.10/100k and would themselves be a cost on
  a high-request bucket. The trade needs pricing against the map's 565k-request month.
- **What is CloudZero's actual spend-to-alert latency?** Docs claim hourly-granularity detection but
  publish no lag figure; AnyCost ingest is documented at up to 24h. Unresolved.
- **Harness's numbers need re-verification.** Several `developer.harness.io` CCM URLs 404'd at
  retrieval; "Prophet, 42 days" is corroborated only by search-indexed snippets of that domain.
- **Is a member-account Data Export sufficient without payer-side tag activation?** The export can
  be created, but `resource_tags_*` columns only populate for tags the payer has activated — so the
  hourly resource-id spine is reachable while the tag spine is not. The scope model needs to decide
  what attribution means with resource IDs but no tags.
- **CloudFront per-request pricing** could not be confirmed from the AWS pricing page as rendered;
  the mechanism (per-10,000-requests aggregate) is consistent, the figure is unverified.
