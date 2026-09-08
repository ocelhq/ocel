import { UP_TITLE } from "../plan";
import { LINK_QUERY_ROW, LINK_ROW, nextCacheRows, nextDataCacheRows } from "../rows";
import type { Gap } from "./types";

const SDK_NODE_HTTP = ["sdk/node/web"];
const DEPLOY_WORKSPACE = ["deploy/workspace/next", "deploy/workspace/express"];
const SDK_WORKSPACE = ["sdk/workspace/next", "sdk/workspace/express"];
const SDK_CELLS = [...SDK_NODE_HTTP, "sdk/next/web", ...SDK_WORKSPACE];
const NEXT_CELLS = ["deploy/next/web", "sdk/next/web"];
const LIFECYCLE_CELLS = ["lifecycle/next/web"];
const DEPLOY_NEXT_BEARING = ["deploy/next/web", ...DEPLOY_WORKSPACE];
const SDK_NEXT_BEARING = ["sdk/next/web", ...SDK_WORKSPACE];
const EVERY_NEXT_BEARING_CELL = [...DEPLOY_NEXT_BEARING, ...LIFECYCLE_CELLS, ...SDK_NEXT_BEARING];

const BASE = ["base"];
const GATEWAY = ["api-gateway"];
const CONTAINER = ["container"];

const EVERY_NEXT_CACHE_ROW = [...nextCacheRows, ...nextDataCacheRows];

export const gaps: Gap[] = [
  {
    id: "no-router-in-front-of-dev",
    reason: "ocel dev does not front a Next app with the router, so no cache tier is observable",
    issue: 898,
    affects: [
      {
        on: ["dev", "dev-local"],
        cells: NEXT_CELLS,
        tests: [{ rows: EVERY_NEXT_CACHE_ROW, legs: ["contract"] }],
      },
    ],
  },
  {
    id: "no-bucket-on-a-box",
    reason: "the vps provider serves no bucket, so every sdk fixture is refused at up",
    issue: 918,
    affects: [
      {
        on: ["vps", "vps.incus"],
        cells: [...LIFECYCLE_CELLS, ...SDK_CELLS],
        variants: BASE,
        tests: [UP_TITLE],
        skip: true,
      },
    ],
  },
  {
    id: "no-router-on-a-box",
    reason: "vps serves a Next app from a container behind Caddy, with no cache router in front",
    issue: 900,
    affects: [
      {
        on: ["vps", "vps.incus"],
        cells: [...NEXT_CELLS, ...LIFECYCLE_CELLS],
        variants: BASE,
        tests: [{ rows: EVERY_NEXT_CACHE_ROW }],
      },
    ],
  },
  {
    id: "sst-util-global",
    reason: "@ocel/sst reads $util off globalThis, which SST 3.19 does not set",
    issue: 857,
    affects: [
      {
        on: ["aws", "aws.floci"],
        cells: ["sdk/with-sst/web"],
        tests: [UP_TITLE],
        skip: true,
      },
    ],
  },
  {
    id: "pulumi-provider-serialisation",
    reason: "@ocel/pulumi's dynamic provider cannot be serialised, so pulumi up fails at preview",
    issue: 856,
    affects: [
      {
        on: ["aws", "aws.floci"],
        cells: ["sdk/with-pulumi/web"],
        tests: [UP_TITLE],
        skip: true,
      },
    ],
  },
  {
    id: "migrate-needs-link",
    reason:
      "the aws journey migrates through ocel run, which needs a console link the lane never has",
    issue: 911,
    affects: [{ on: ["aws"], cells: SDK_NODE_HTTP, tests: [UP_TITLE], skip: true }],
  },
  {
    id: "build-needs-postgres",
    reason: "ocel build fails collecting page data for /api/todos without a resolved postgres",
    issue: 849,
    affects: [
      {
        on: ["aws"],
        cells: [...LIFECYCLE_CELLS, ...SDK_NEXT_BEARING],
        tests: [UP_TITLE],
        skip: true,
      },
    ],
  },
  {
    id: "cloudfront-answers-403",
    reason: "once the hostname's route resolves, every request through cloudfront answers 403",
    issue: 923,
    affects: [
      {
        on: ["aws"],
        cells: DEPLOY_NEXT_BEARING,
        variants: BASE,
        tests: [UP_TITLE],
        skip: true,
      },
      { on: ["aws"], variants: CONTAINER, tests: [UP_TITLE], skip: true },
    ],
  },
  {
    id: "link-query-hangs",
    reason: "a select through the postgres link answers an HTML error after ~15s on a real account",
    issue: 925,
    affects: [
      {
        on: ["aws"],
        cells: ["sdk/with-transforms/web"],
        variants: GATEWAY,
        tests: [{ row: LINK_QUERY_ROW }],
      },
    ],
  },
  {
    id: "stale-release-after-deploy",
    reason:
      "the first request after ocel deploy still answers with the previous release's environment",
    issue: 926,
    affects: [
      {
        on: ["aws"],
        cells: ["sdk/with-transforms/web"],
        variants: GATEWAY,
        tests: [{ row: LINK_ROW, legs: ["redeploy"] }],
      },
    ],
  },
  {
    id: "no-master-secret",
    reason:
      "an RDS cluster with ManageMasterUserPassword reports no master user secret under floci",
    issue: 884,
    affects: [
      {
        on: ["aws.floci"],
        cells: [...SDK_NODE_HTTP, "sdk/with-transforms/web"],
        variants: GATEWAY,
        tests: [UP_TITLE],
        skip: true,
      },
    ],
  },
  {
    id: "floci-runs-no-load-balancer",
    reason:
      "the aws provider runs a container on Fargate behind a load balancer floci has no data plane for",
    issue: 995,
    affects: [{ on: ["aws.floci"], variants: ["container"], tests: [UP_TITLE], skip: true }],
  },
  {
    id: "next-on-cloud-run",
    reason: "gcp phase 3 serves node, go and python; a Next app has no router in front of it there",
    issue: 1097,
    affects: [
      {
        on: ["gcp", "gcp.floci"],
        cells: DEPLOY_NEXT_BEARING,
        tests: [UP_TITLE],
        skip: true,
      },
    ],
  },
  {
    id: "cloudfront-stub",
    reason: "floci's CloudFront bootstrap resources are not backed by the CloudFront API",
    issue: 852,
    affects: [
      {
        on: ["aws.floci"],
        cells: EVERY_NEXT_BEARING_CELL,
        variants: BASE,
        tests: [UP_TITLE],
        skip: true,
      },
    ],
  },
  {
    id: "no-cloudflare-api",
    reason: "nothing stands in for the Cloudflare API under floci",
    issue: 904,
    affects: [
      {
        on: ["aws.floci"],
        cells: EVERY_NEXT_BEARING_CELL,
        variants: ["cloudflare"],
        tests: [UP_TITLE],
        skip: true,
      },
    ],
  },
];
