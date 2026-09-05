import { DESTROY_TITLE, UP_TITLE } from "../plan";
import {
  EDGE_ISR_TITLE,
  LINK_QUERY_ROW,
  LINK_ROW,
  nextCacheRows,
  nextDataCacheRows,
  PREFETCH_TITLE,
  STREAM_ROW,
  UPLOAD_ROW,
} from "../rows";
import type { Gap } from "./types";

const DEPLOY_NODE_HTTP = ["deploy/node/web"];
const SDK_NODE_HTTP = ["sdk/node/web"];
const DEPLOY_WORKSPACE = ["deploy/workspace/next", "deploy/workspace/express"];
const SDK_WORKSPACE = ["sdk/workspace/next", "sdk/workspace/express"];
const DEPLOY_CELLS = [...DEPLOY_NODE_HTTP, "deploy/next/web", ...DEPLOY_WORKSPACE];
const SDK_CELLS = [...SDK_NODE_HTTP, "sdk/next/web", ...SDK_WORKSPACE];
const NEXT_CELLS = ["deploy/next/web", "sdk/next/web"];
const EVERY_AWS_CELL_BUT_LADDERS = [...DEPLOY_CELLS, ...SDK_CELLS, "sdk/with-transforms/web"];

const BASE = ["base"];
const SERVERLESS = ["base", "api-gateway", "cloudflare"];

const EVERY_NEXT_CACHE_ROW = [...nextCacheRows, ...nextDataCacheRows];
const EVERY_NEXT_CACHE_ROW_WITH_A_ROUTER = EVERY_NEXT_CACHE_ROW.filter(
  (row) => row.title !== PREFETCH_TITLE,
);

export const gaps: Gap[] = [
  {
    id: "env-set-needs-provider",
    reason: "ocel env set demands a provider, so dev cannot deliver GREETING or SECRET_TOKEN",
    issue: 881,
    affects: [{ on: ["dev"], cells: SDK_CELLS, tests: [UP_TITLE], skip: true }],
  },
  {
    id: "no-project-delete",
    reason: "the console has no project delete, so destroy leaves the project behind",
    issue: 877,
    affects: [{ on: ["dev"], tests: [DESTROY_TITLE] }],
  },
  {
    id: "dev-upload-key-namespaced",
    reason: "the dev blob store namespaces an upload key, so the documents/ prefix assertion fails",
    issue: 882,
    affects: [
      { on: ["dev"], cells: SDK_CELLS, tests: [{ row: UPLOAD_ROW, legs: ["contract"] }] },
    ],
  },
  {
    id: "no-router-in-front-of-dev",
    reason: "ocel dev does not front a Next app with the router, so no cache tier is observable",
    issue: 898,
    affects: [
      {
        on: ["dev"],
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
        cells: SDK_CELLS,
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
        cells: NEXT_CELLS,
        variants: BASE,
        tests: [{ rows: EVERY_NEXT_CACHE_ROW_WITH_A_ROUTER }],
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
        variants: SERVERLESS,
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
        variants: SERVERLESS,
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
    affects: [
      { on: ["aws"], cells: SDK_NODE_HTTP, variants: SERVERLESS, tests: [UP_TITLE], skip: true },
    ],
  },
  {
    id: "build-needs-postgres",
    reason: "ocel build fails collecting page data for /api/todos without a resolved postgres",
    issue: 849,
    affects: [
      {
        on: ["aws"],
        cells: ["sdk/next/web", ...SDK_WORKSPACE],
        variants: SERVERLESS,
        tests: [UP_TITLE],
        skip: true,
      },
      {
        on: ["aws.floci"],
        cells: ["sdk/next/web", ...SDK_WORKSPACE],
        variants: ["api-gateway"],
        tests: [UP_TITLE],
        skip: true,
      },
    ],
  },
  {
    id: "no-edge-cache-on-api-gateway",
    reason: "a Next app refuses to deploy behind the api-gateway edge because it needs edge-cache",
    issue: 906,
    affects: [
      {
        on: ["aws", "aws.floci"],
        cells: ["deploy/next/web", ...DEPLOY_WORKSPACE],
        variants: ["api-gateway"],
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
        cells: [...DEPLOY_CELLS, "sdk/with-transforms/web"],
        variants: BASE,
        tests: [UP_TITLE],
        skip: true,
      },
    ],
  },
  {
    id: "cloudflare-no-deployment-yet",
    reason: "a bound cloudflare hostname answers No deployment yet after two deploys",
    issue: 922,
    affects: [
      {
        on: ["aws"],
        cells: ["sdk/with-transforms/web"],
        variants: ["cloudflare"],
        tests: [UP_TITLE],
        skip: true,
      },
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
        variants: ["api-gateway"],
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
        variants: ["api-gateway"],
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
        variants: ["api-gateway"],
        tests: [UP_TITLE],
        skip: true,
      },
    ],
  },
  {
    id: "stage-variables",
    reason:
      "floci's API Gateway substitutes no stage variables and serves no streaming path, so the edge answers 400",
    issue: 854,
    affects: [
      {
        on: ["aws.floci"],
        cells: EVERY_AWS_CELL_BUT_LADDERS,
        variants: ["api-gateway"],
        tests: [{ rows: "every", except: [STREAM_ROW, EDGE_ISR_TITLE] }],
      },
    ],
  },
  {
    id: "no-streamed-body",
    reason: "a floci Function URL never delivers the body of a streamed response and hangs",
    issue: 851,
    affects: [{ on: ["aws.floci"], variants: ["api-gateway"], tests: [{ row: STREAM_ROW }] }],
  },
  {
    id: "edge-runtime-isr",
    reason: "a Next page with runtime edge and a revalidate never serves a cached tier",
    issue: 899,
    affects: [
      {
        on: ["aws.floci"],
        cells: NEXT_CELLS,
        variants: ["api-gateway"],
        tests: [{ row: EDGE_ISR_TITLE }],
      },
    ],
  },
  {
    id: "aws-container-unimplemented",
    reason: "the aws provider advertises serverless only, so preflight refuses a container app",
    issue: 937,
    affects: [
      { on: ["aws", "aws.floci"], variants: ["container"], tests: [UP_TITLE], skip: true },
    ],
  },
  {
    id: "cloudfront-stub",
    reason: "floci's CloudFront bootstrap resources are not backed by the CloudFront API",
    issue: 852,
    affects: [
      {
        on: ["aws.floci"],
        cells: EVERY_AWS_CELL_BUT_LADDERS,
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
        cells: EVERY_AWS_CELL_BUT_LADDERS,
        variants: ["cloudflare"],
        tests: [UP_TITLE],
        skip: true,
      },
    ],
  },
];
