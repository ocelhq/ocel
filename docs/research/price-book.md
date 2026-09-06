# The price book: turning metered usage into dollars before the bill

Research for [#1014](https://github.com/ocelhq/ocel/issues/1014), part of the cost & budget map [#1010](https://github.com/ocelhq/ocel/issues/1010).
All facts retrieved **2026-09-06**. Prices are us-east-1 list, from the offer-file versions named inline.

## Headlines

1. The AWS Price List **Bulk API needs no credentials at all** — `https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/...` served 200 to unauthenticated curl for every file measured below. The *Query* API (`pricing:GetProducts`) needs IAM but zero billing permissions.
2. Offer files are brutally lopsided: AmazonCloudFront `index.json` is **226 KB**, AWSLambda **13.6 MB**, AmazonS3 **12.6 MB**, AWSDataTransfer **59.1 MB**, AmazonEC2 **9.07 GB** (us-east-1 alone: **482 MB** JSON / **304 MB** CSV) — for the six NAT rows ocel actually needs.
3. A curated snapshot of exactly the SKUs ocel prices — Lambda, S3, DynamoDB on-demand, CloudFront, inter-region/internet DTO — is **2,172 rows, 221 KB JSON, 13 KB gzipped, across 51 regions**. Measured, not estimated.
4. **Free tier and tiered pricing are already in the price list** as `$0.00` and stepped `StartingRange`/`EndingRange` bands: DynamoDB storage `0–25 GB-Mo @ $0`, `Global-DataTransfer-Out-Bytes 0–100 GB @ $0`, Lambda GB-s in three tiers, CloudFront DTO in seven.
5. Price changes are pushed: SNS `arn:aws:sns:us-east-1:278350005181:price-list-api` (per change) and `:daily-aggregated-price-list-api` (daily), each carrying `offerCode`, `version`, and the current URLs — so refresh is event-driven, not polled.
6. **The fluke day arithmetic works from list price alone.** `Requests-Tier1 = $0.000005/request`; 348k Tier-1 requests × $0.000005 = **$1.74**, matching the map's Aug 9 figure exactly. No CUR, no tags, no bill.
7. **Infracost's Cloud Pricing API is not an option.** `github.com/infracost/cloud-pricing-api` is 404, self-hosting moved to a paid plan in 2023, and Infracost's ToS §9 forbids commercial redistribution of the data. The CLI remains Apache-2.0 and its tier/free-tier handling is worth copying, not its data.
8. **CUR-derived effective rates must group by `pricing_rate_code`, not `line_item_usage_type`** — tiers share a usage type — and `line_item_unblended_rate` is `0` on `DiscountedUsage` rows. Naive `cost ÷ usage` produces a blended number matching no published rate.
9. **Standalone-first inverts the sketch's data-source order.** A member account *can* self-provision CUR 2.0 (scoped to itself) but *cannot* self-enable Cost Explorer — that is an all-or-nothing payer switch. CE hourly also retains only 14 days and bills $0.01/paginated request.
10. **Recommendation: bundle the 13 KB snapshot in the binary, verify it against `ListPriceLists` at runtime, and treat CUR as a rate *correction* that never gates the estimate.** Kubecost's shape (estimate now at list, reconcile in place at 48 h) is the only prior art with a published accuracy figure — 3–5%.

---

## 1. The AWS Price List Bulk API

Two surfaces share one dataset. The **Bulk API** is static files under `https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/`; the **Query API** is `pricing:DescribeServices` / `GetAttributeValues` / `GetProducts` / `ListPriceLists` / `GetPriceListFileUrl`, documented at [using-the-aws-price-list-bulk-api](https://docs.aws.amazon.com/awsaccountbilling/latest/aboutv2/using-the-aws-price-list-bulk-api.html) and [using-price-list-query-api](https://docs.aws.amazon.com/awsaccountbilling/latest/aboutv2/using-price-list-query-api.html).

### Auth

The IAM policy AWS publishes for the price APIs ([billing-example-policies](https://docs.aws.amazon.com/awsaccountbilling/latest/aboutv2/billing-example-policies.html), § *Find products and prices*) is verbatim:

```json
{"Effect":"Allow",
 "Action":["pricing:DescribeServices","pricing:GetAttributeValues","pricing:GetProducts",
           "pricing:GetPriceListFileUrl","pricing:ListPriceLists"],
 "Resource":["*"]}
```

No `aws-portal:*`, no `ce:*`, no `cur:*` — **pricing is not a billing permission**. Prices are public; nothing about them is account-specific.

More useful still: the bulk files themselves are unauthenticated object reads. Every `curl` in this document ran with no AWS credentials present and returned `200`. The docs say an IAM identity "must have permission to use the Price List Query API or Price List Bulk API"; empirically that applies to the `pricing:GetPriceListFileUrl` *discovery* call, not to the `pricing.us-east-1.amazonaws.com` URLs it hands back. **This is the single most important fact for standalone-first**: ocel can price usage in an account it has no credentials for at all.

### Offer-file sizes (HTTP `Content-Length`, 2026-09-06)

| Offer | global `index.json` | `us-east-1/index.json` | `us-east-1/index.csv` |
|---|---|---|---|
| AmazonCloudFront | 225,750 | 7,089 | 2,831 |
| AmazonDynamoDB | 1,438,452 | 43,463 | 17,979 |
| AmazonS3 | 12,604,078 | 473,422 | 159,615 |
| AWSLambda | 13,629,255 | 675,747 | 214,208 |
| AmazonCloudWatch | 5,388,252 | — | — |
| AWSDataTransfer | 59,134,661 | — | 481,704 |
| **AmazonEC2** | **9,071,234,824** | **481,907,939** | 303,506,818 |

`Last-Modified` on AmazonEC2 was `2026-09-05`; on the others `2026-08-31`. CSV is roughly 3× smaller than JSON for the same content and is trivially streamable — `curl | grep` over the 304 MB EC2 CSV extracted the NAT rows without ever holding the file.

EC2 is the shape of the problem. ocel needs **six rows** from it (NAT Gateway hours and data processed) and would pay 9 GB, or 482 MB region-scoped, to get them. Any design that fetches whole offer files at runtime is dead on this row alone.

### Freshness

Files carry `Version` (e.g. `20260831092318`) and `Publication Date` in their header, and `region_index.json` (3–7 KB per service) maps regions to versioned URLs. Rather than poll, subscribe: [notifications-price-list-api](https://docs.aws.amazon.com/awsaccountbilling/latest/aboutv2/notifications-price-list-api.html) gives

- `arn:aws:sns:us-east-1:278350005181:price-list-api` — one message per price change
- `arn:aws:sns:us-east-1:278350005181:daily-aggregated-price-list-api` — daily rollup
- `arn:aws:sns:us-east-1:626627529009:SavingsPlanPublishNotifications`

with message body `{formatVersion, offerCode, version, timeStamp, url:{JSON,CSV}, regionIndex, operation:"Publish"}`. AWS states "price list files can change anytime" and recommends subscribing. `ListPriceLists --effective-date` retrieves historical versions, so a stale bundled snapshot is reproducible and diffable.

## 2. The SKUs ocel actually needs

Extracted from the region-scoped `index.csv` files above. `PricePerUnit` is us-east-1 USD, list, on-demand.

**Lambda** (`AWSLambda`, `Product Family = Serverless`) — tiered, and ARM is a *different usage type*, not an attribute:

| usageType | unit | range | $/unit |
|---|---|---|---|
| `Request` / `Request-ARM` | Requests | 0–Inf | 0.0000002 |
| `Lambda-GB-Second` | Lambda-GB-Second | 0–6e9 / 6e9–15e9 / 15e9–Inf | 0.0000166667 / 0.000015 / 0.0000133334 |
| `Lambda-GB-Second-ARM` | Lambda-GB-Second | 0–7.5e9 / 7.5e9–18.75e9 / 18.75e9–Inf | 0.0000133334 / 0.0000120001 / 0.0000106667 |
| `Lambda-Provisioned-Concurrency(-ARM)` | Lambda-GB-Second | 0–Inf | 0.0000041667 / 0.0000033334 |
| `Lambda-Provisioned-GB-Second(-ARM)` | Lambda-GB-Second | 0–Inf | 0.0000097222 / 0.0000077778 |
| `Lambda-Storage-GB-Second(-ARM)` | GB-Seconds | 0–Inf | 0.0000000309 |
| `Lambda-Edge-Request` / `Lambda-Edge-GB-Second` | — | 0–Inf | 0.0000006 / 0.00005001 |

**S3** (`AmazonS3`):

| usageType | family | range | $/unit |
|---|---|---|---|
| `TimedStorage-ByteHrs` | Storage | 0–51200 / 51200–512000 / 512000–Inf GB-Mo | 0.023 / 0.022 / 0.021 |
| `TimedStorage-ZIA-ByteHrs` | Storage | 0–Inf GB-Mo | 0.010 |
| `Requests-Tier1` | API Request | 0–Inf | 0.000005 |
| `Requests-Tier2` | API Request | 0–Inf | 0.0000004 |
| `Requests-ZIA-Tier1` | API Request | 0–Inf | 0.000010 |
| `Requests-ZIA-Tier2` | API Request | 0–Inf | 0.000001 |
| `Retrieval-ZIA` | API Request | 0–Inf GB | 0.010 |
| `EarlyDelete-ZIA` | Fee | 0–Inf GB-Mo | 0.010 |

One Zone-IA is **2× Standard on requests and 0.43× on storage** — precisely the inversion behind the map's fluke day, where 92% of S3 cost was requests. And the arithmetic closes: 348,000 Tier-1 requests × $0.000005 = **$1.74**, the exact Aug 9 figure. A price book of two dozen rows reproduces that number with no CUR, no tags, and no bill.

**DynamoDB on-demand** (`AmazonDynamoDB`, `Product Family = Amazon DynamoDB PayPerRequest Throughput`): `ReadRequestUnits` $0.000000125, `WriteRequestUnits` / `ReplWriteRequestUnits` $0.000000625; IA class `IA-ReadRequestUnits` $0.000000155, `IA-WriteRequestUnits` $0.00000078. Storage: `TimedStorage-ByteHrs` **0–25 GB-Mo @ $0.00**, 25–Inf @ $0.25; `IA-TimedStorage-ByteHrs` $0.10 flat. Provisioned mode shows the same free-tier shape — `ReadCapacityUnit-Hrs` 0–18600 @ $0 (25 RCU × 744 h), 18600–Inf @ $0.00013.

**CloudFront** (`AmazonCloudFront`): the regional file holds only Origin Shield and Lambda@Edge; the billable edge rates live in the **global** file keyed by edge-location group. `US-Requests-Tier1` $0.00000075, `US-Requests-Tier2-HTTPS`/`US-Requests-HTTPS-Proxy` $0.000001, `EU-Requests-Tier1` $0.0000009. `US-DataTransfer-Out-Bytes` runs seven tiers: `0.085 / 0.080 / 0.060 / 0.040 / 0.030 / 0.025 / 0.020` at 10/50/150/500/1024/5120 TB boundaries.

**Data transfer** (`AWSDataTransfer`): `DataTransfer-Out-Bytes` (internet) 0–10240 GB $0.09, 10240–51200 $0.085, 51200–153600 $0.07, 153600–Inf $0.05; `DataTransfer-Regional-Bytes` $0.01; `Global-DataTransfer-Out-Bytes` **0–100 GB @ $0.00** — the 100 GB/month free allowance, expressed as an ordinary tier. Inter-region is `<SRC>-<DST>-AWS-Out-Bytes`, ~$0.02/GB from us-east-1.

**NAT** (`AmazonEC2`, `Product Family = NAT Gateway`): `$0.045 per NAT Gateway Hour` and `$0.045 per GB Data Processed by NAT Gateways`; provisioned-bandwidth variants at $1.076/Gbps-hr.

### The measured snapshot

Filtering the four global offer CSVs plus AWSDataTransfer to on-demand rows whose `usageType` matches that SKU set, across every region:

| offer | rows kept / total |
|---|---|
| AWSLambda | 547 / 10,620 |
| AmazonS3 | 658 / 9,943 |
| AmazonDynamoDB | 322 / 1,296 |
| AmazonCloudFront | 117 / 212 |
| AWSDataTransfer | 528 / 41,757 |

**2,172 rows · 221,272 bytes JSON · 13,089 bytes gzipped · 51 regions · 1,198 distinct usage types.** Add ~100 NAT rows and it is still under 15 KB compressed. That is the whole price book, and it fits in a Go `embed.FS` without anyone noticing.

## 3. Infracost's Cloud Pricing API

**The repo is gone.** `github.com/infracost/cloud-pricing-api` returns 404 via web and API, and is absent from the org listing; `infracost/helm-charts` is likewise 404. The reason is on the record from Infracost's co-founder in [infracost/infracost#2945](https://github.com/infracost/infracost/discussions/2945) (2024-03-15): *"the self-hosting option moved to the paid plan last year."* The self-hosting docs now return a 283-byte stub; the Docker image `infracost/cloud-pricing-api` last shipped `0.3.19` on 2024-08-06.

Surviving Apache-2.0 forks (`mattiarossi`, `IBM-Cloud`, `terrateamio`) show the architecture: a single Postgres table `products(productHash PK, sku, vendorName, region, service, productFamily, attributes jsonb, prices jsonb)` with two btree indexes, loaded by `COPY` from a gzipped CSV into `ProductLoad` and swapped atomically. The supported ingest is not scraping but `GET https://pricing.api.infracost.io/data-download/latest` with `X-Api-Key` — which returned **403 `{"error":"Invalid API key"}`** unauthenticated today. The scrapers that do exist hit the same AWS bulk index ocel would.

The blocker is data, not code. Infracost's [ToS §9](https://www.infracost.io/docs/terms-of-service/): *"You may not distribute, modify, transmit, reuse, download, repost, copy, or use said Content… for commercial purposes or for personal gain, without express advance written permission."* §10(b) separately forbids automated copying. **Do not plan on redistributing Infracost's dump.**

What *is* reusable is the CLI (genuinely Apache-2.0, active) and specifically its tiering and free-tier idioms: `usage.CalculateTierBuckets(quantity, tierLimits)` splits a quantity into per-tier cost components, each selecting its row with `PriceFilter{startUsageAmount, endUsageAmount}`; the open-ended tier is `endUsageAmount: "Inf"`. Free tier is handled three ways, all per-resource — filter the row out by description regex (`^(?!.*\(free tier\)).*$` for DynamoDB), start the filter above the threshold (`StartUsageAmount: "100000"` for SNS), or subtract-and-clamp (Lambda). There is no generic free-tier engine. Given AWS encodes most free tiers as a $0 first band, ocel needs less machinery than Infracost does.

## 4. Effective rates from CUR 2.0

Every column named in the ticket exists in CUR 2.0 ([line item](https://docs.aws.amazon.com/cur/latest/userguide/table-dictionary-cur2-line-item.html), [pricing](https://docs.aws.amazon.com/cur/latest/userguide/table-dictionary-cur2-pricing.html), [product](https://docs.aws.amazon.com/cur/latest/userguide/table-dictionary-cur2-product.html) dictionaries), with three traps:

- **Rate columns are `string`**, cost columns are `double`. `line_item_unblended_rate`, `pricing_public_on_demand_rate`, `line_item_net_unblended_rate` all need casting.
- **`line_item_usage_type` does not encode the tier.** `pricing_rate_code` is documented as "a unique code for a product/offer/**pricing-tier** combination"; the [consolidated-billing worked example](https://docs.aws.amazon.com/awsaccountbilling/latest/aboutv2/con-bill-blended-rates.html#Blended_Rate_Overview) shows one S3 usage type split into three rows at $0.10/$0.08/$0.06. Group by `pricing_rate_code` or you compute a blend that matches no published rate.
- **`pricing_public_on_demand_rate` reports the highest tier** for tiered and free-tier SKUs — AWS says so explicitly. List-vs-effective comparison on a tiered SKU is skewed unless joined tier by tier.

Line-item types are `Usage`, `DiscountedUsage`, `SavingsPlanCoveredUsage`, `SavingsPlanNegation`, `SavingsPlanUpfrontFee`, `SavingsPlanRecurringFee`, `Credit`, `Refund`, `Tax`, `Fee`, `RIFee`, `Discount`, `BundledDiscount`, `FlateRateSubscription` (AWS's spelling). `EdpDiscount` and `PrivateRateDiscount` are **not** line-item types in CUR 2.0 — they are keys in the `discount` map column. On `DiscountedUsage` rows `line_item_unblended_rate` is `0` (the real rate is `reservation_effective_cost`); on `SavingsPlanCoveredUsage` it nets to zero against the paired negation row (real rate: `savings_plan_savings_plan_effective_cost`).

A defensible derivation: filter to `Usage`/`DiscountedUsage`/`SavingsPlanCoveredUsage` with `line_item_usage_amount > 0`, then divide summed cost by summed usage grouped by `(pricing_rate_code, line_item_usage_type, line_item_operation, pricing_unit, product_region_code)`.

**Free tier in CUR is the one thing AWS does not document.** Evidence points to zero-rated `Usage` rows with `freetier`-marked usage types (`Resource-Invocation-Count-FreeTier`, `BoxUsage:freetier.micro`, per [tracking-free-tier-usage](https://docs.aws.amazon.com/awsaccountbilling/latest/aboutv2/tracking-free-tier-usage.html)) *plus* `Credit` rows under the 2025-onward credit-based plans. Verify against a real export.

**Availability, and the standalone-first inversion.** A member account **can** create its own CUR 2.0, automatically scoped to itself: *"If you're using a member account, your CUR 2.0 table only includes cost and usage data for that member account"* ([CUR 2.0 table](https://docs.aws.amazon.com/cur/latest/userguide/table-dictionary-cur2.html#cur2-table-organizations)). It needs `bcm-data-exports:CreateExport` **plus** `cur:PutReportDefinition`; a payer SCP denying `cur:*` silently blocks it. First delivery takes up to 24 h, refresh is "at least once a day" with no cadence SLA, the current month is rewritten wholesale, and restatement continues up to two weeks past month end. Org history is not portable across an org change.

Cost Explorer is the *worse* standalone dependency, not the better one. `ce:GetCostAndUsage` supports `HOURLY`, but hourly data is retained **14 days** and billed at $0.01 per 1,000 usage records/month on top of **$0.01 per paginated request**. Decisively: *"A management account can grant access to Cost Explorer for all or none of the member accounts"* ([ce-access](https://docs.aws.amazon.com/cost-management/latest/userguide/ce-access.html)) — a single payer-side switch ocel cannot touch, the same class of blocker as the inaccessible cost-allocation tags already recorded on the map. CE also returns no unit-price field at all, so it cannot yield rates.

FOCUS is the better rate substrate where available: AWS ships `FOCUS_1_0_AWS` and `FOCUS_1_2_AWS` (no 1.1) with `ListUnitPrice`, `ContractedUnitPrice`, `EffectiveCost` as typed `double` columns per the [FOCUS spec](https://focus.finops.org/focus-columns/). Only 1.2 has `TIME_GRANULARITY`. Read AWS's conformance-gap pages before depending on `ListUnitPrice`.

## 5. What Vantage and Kubecost do

**OpenCost/Kubecost** fetch the same bulk file ocel would. `pkg/cloud/aws/provider.go` in [opencost/opencost](https://github.com/opencost/opencost/blob/develop/pkg/cloud/aws/provider.go) hardcodes `awsPricingBaseURL = "https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/"` and streams `AmazonEC2/current/<region>/index.json`, caching only the subset matching current nodes, in memory, with no TTL — refetched on process start, on an unknown node, or via `POST /refreshPricing`. `AWS_PRICING_URL` overrides it for airgapped installs.

Reconciliation replaces those list estimates with billed rates from the CUR via Athena, in place: *"reconciliation requires a complete day of cost data… This requires a **48-hour window** between resource usage and reconciliation. If reconciliation is performed within this window, asset cost is deflated."* Unreconciled data is a UI flag, not a separate series — "the most recent 36 hours of data will display hatching". Kubecost's only published accuracy claim is *"within a 3-5% margin of error"*, and it describes post-reconciliation data. OpenCost without Kubecost has **no** CUR reconciliation at all; EDP-style discounts are a hand-entered `negotiatedDiscount` percentage.

**Vantage takes the opposite stance: it never estimates.** It ingests CUR (detail) plus Cost Explorer (immediate history), refreshes AWS "every 8–12 hours", and clips every report to **two days before today** because recent days are incomplete. Even its Kubernetes agent, which meters pods, waits for the underlying infrastructure bill — "often 48 hours". Its forward-looking product is forecasting, not metering. Vantage's one openly published price dataset is `vantage-sh/ec2instances.info` (MIT code; the scraped data itself carries no separate licence), fetchable at `https://instances.vantage.sh/instances.json`.

For ocel, whose stated floor is catching a $1.74 anomaly *on the day it happens*, Vantage's shape is disqualified by construction. Kubecost's is the reference.

## 6. Recommendation

**Ship a bundled snapshot; verify at runtime; correct from CUR; never gate on either.**

1. **Bundle.** Embed the curated snapshot — 2,172 rows, 13 KB gzipped, 51 regions — in the `ocel` binary. It is smaller than most icons in the console. It makes `ocel cost` work offline, in an account ocel has no credentials for, on first run, with no bootstrap. Generate it in `scripts/` from the region-scoped `index.csv` files, keyed `(serviceCode, regionCode, usageType, operation, unit, startRange, endRange, pricePerUnit)`, and commit the `Version` string of each source offer file beside it so the snapshot is reproducible and diffable.
2. **Verify, don't fetch.** At runtime, call `ListPriceLists` (or read `region_index.json` unauthenticated) and compare the `Version` of each source offer against the bundled one. On drift, warn and optionally refresh the affected offer — never block. Do **not** fetch whole offer files eagerly: EC2 alone is 9 GB.
3. **Refresh on release.** Regenerate in CI from the daily SNS aggregate. AWS price changes are rare and near-universally downward; a snapshot stale by a release cycle overstates cost, which is the safe direction for a guard.
4. **Model tiers as first-class.** Rows carry `startRange`/`endRange`; the evaluator buckets a quantity across them, Infracost-style. Free tier needs no special case where AWS already encodes it as a $0 band — DynamoDB storage, DTO's first 100 GB, provisioned RCU/WCU. Where it does not (S3 requests, Lambda), free-tier allowances belong to the *account*, not the price book, and should be a separate, explicitly-optional subtraction: applying them silently is how a per-app estimate becomes wrong the moment a second app shares the account.
5. **CUR corrects, never gates.** When a CUR exists, derive effective rates per §4 and use them to *override* list rows for that account — a rate-override layer keyed by `pricing_rate_code`, not a second cost pipeline. Adopt Kubecost's honesty: mark data younger than 48 h as unreconciled and say so. Reject Vantage's two-day clip outright; it is exactly the window in which the fluke day must be caught.
6. **Do not build on Infracost.** The server is withdrawn and the data is contractually off-limits. Ingest AWS directly.

The load-bearing consequence for the map: **the price book is not the hard part.** It is 13 KB and needs no credentials. The hard part is the usage side — getting request counts for a bucket with no CloudWatch request metrics enabled — and that belongs to the metering and scope tickets, not here.

## Open questions

- **Free tier in CUR 2.0.** AWS does not document the `line_item_line_item_type` for free-tier rows. Confirm against a real export whether they are zero-rated `Usage` rows, `Credit` rows, or both under the 2025+ credit-based plans.
- **Account-level free-tier state.** Nothing in the price list says how much of an allowance is consumed. Whether ocel models this at all — and whether a per-app estimate should ever net it — is a scope-model decision, not a price-book one.
- **Bulk API terms.** The offer files carry a disclaimer ("for informational purposes only… subject to the additional terms included in the pricing pages") but no explicit redistribution grant. Bundling a derived snapshot in a distributed binary is standard practice (OpenCost, ec2instances.info) but is not covered by an affirmative licence I could find. Worth a lawyer's five minutes before 1.0.
- **AWS's FOCUS conformance gaps** (`table-dictionary-focus-1-{0,2}-aws-conformance`) were not read; they are where AWS would disclose null or wrong `ListUnitPrice` values.
- **Provisioned-bandwidth NAT and Lambda Managed Instances** appeared in the offer files (`$1.076/Gbps-hr`; ~1,900 `Lambda-Managed-Instances-*` rows) and are excluded from the snapshot. Confirm ocel never provisions them before that exclusion hardens.
