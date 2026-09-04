import { EDGE_ISR_TITLE } from "../nextCache";
import { DESTROY_TITLE, REDEPLOY_TITLE, UP_TITLE } from "../plan";
import type { Gap } from "./types";

const NODE_HTTP = ["express/web", "hono/web", "fastify/web"];
const COMPOSITE = [...NODE_HTTP, "next/web"];
const HELLO = ["express-hello/web", "hono-hello/web", "fastify-hello/web", "next-hello/web"];
const HELLO_WORKSPACE = ["workspace-hello/next", "workspace-hello/express"];
const WORKSPACE = ["workspace/next", "workspace/express"];
const EVERY_AWS_CELL_BUT_LADDERS = [
  ...COMPOSITE,
  ...HELLO,
  ...HELLO_WORKSPACE,
  ...WORKSPACE,
  "with-transforms/web",
];

const UPLOAD_ROW = "the upload protocol stores a document and /api/documents lists it";
const STREAM_ROW = "GET /api/probes/stream streams its chunks in order to the sentinel";
const LINK_QUERY_ROW = "GET /api/link/query answers ok after a select through the link";
const LINK_ROW = "GET /api/link answers with what it resolved and the greeting it deployed with";

export const gaps: Gap[] = [
  {
    id: "env-set-needs-provider",
    reason: "ocel env set demands a provider, so dev cannot deliver GREETING or SECRET_TOKEN",
    issue: 881,
    affects: [{ on: ["dev"], tests: [UP_TITLE] }],
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
    affects: [{ on: ["dev"], tests: [{ row: UPLOAD_ROW, legs: ["contract"] }] }],
  },
  {
    id: "no-router-in-front-of-dev",
    reason: "ocel dev does not front a Next app with the router, so no cache tier is observable",
    issue: 898,
    affects: [
      { on: ["dev"], cells: ["next/web"], tests: [{ rows: ["next-cache"], legs: ["contract"] }] },
    ],
  },
  {
    id: "no-bucket-on-a-box",
    reason: "the vps provider serves no bucket, so every composite example is refused at up",
    issue: 918,
    affects: [{ on: ["vps", "vps.incus"], cells: [...COMPOSITE, ...WORKSPACE], tests: [UP_TITLE] }],
  },
  {
    id: "no-router-on-a-box",
    reason: "vps serves a Next app from a container behind Caddy, with no cache router in front",
    issue: 900,
    affects: [{ on: ["vps", "vps.incus"], cells: ["next/web"], tests: [{ rows: ["next-cache"] }] }],
  },
  {
    id: "sst-util-global",
    reason: "@ocel/sst reads $util off globalThis, which SST 3.19 does not set",
    issue: 857,
    affects: [{ on: ["aws", "aws.floci"], cells: ["with-sst/web"], tests: [UP_TITLE] }],
  },
  {
    id: "pulumi-provider-serialisation",
    reason: "@ocel/pulumi's dynamic provider cannot be serialised, so pulumi up fails at preview",
    issue: 856,
    affects: [{ on: ["aws", "aws.floci"], cells: ["with-pulumi/web"], tests: [UP_TITLE] }],
  },
  {
    id: "migrate-needs-link",
    reason:
      "the aws journey migrates through ocel run, which needs a console link the lane never has",
    issue: 911,
    affects: [{ on: ["aws"], cells: NODE_HTTP, tests: [UP_TITLE] }],
  },
  {
    id: "build-needs-postgres",
    reason: "ocel build fails collecting page data for /api/todos without a resolved postgres",
    issue: 849,
    affects: [
      { on: ["aws"], cells: ["next/web", ...WORKSPACE], tests: [UP_TITLE] },
      {
        on: ["aws.floci"],
        edge: ["api-gateway"],
        cells: ["next/web", ...WORKSPACE],
        tests: [UP_TITLE],
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
        edge: ["api-gateway"],
        cells: ["next-hello/web", ...HELLO_WORKSPACE],
        tests: [UP_TITLE],
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
        edge: ["cloudfront"],
        cells: [...HELLO, ...HELLO_WORKSPACE, "with-transforms/web"],
        tests: [UP_TITLE],
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
        edge: ["cloudflare"],
        cells: [...HELLO, ...HELLO_WORKSPACE, "with-transforms/web"],
        tests: [UP_TITLE],
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
        edge: ["api-gateway"],
        cells: ["with-transforms/web"],
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
        edge: ["api-gateway"],
        cells: ["with-transforms/web"],
        tests: [{ row: LINK_ROW, legs: [REDEPLOY_TITLE] }],
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
        edge: ["api-gateway"],
        cells: [...NODE_HTTP, "with-transforms/web"],
        tests: [UP_TITLE],
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
        edge: ["api-gateway"],
        cells: EVERY_AWS_CELL_BUT_LADDERS,
        tests: [{ rows: "every", except: [STREAM_ROW, EDGE_ISR_TITLE] }],
      },
    ],
  },
  {
    id: "no-streamed-body",
    reason: "a floci Function URL never delivers the body of a streamed response and hangs",
    issue: 851,
    affects: [{ on: ["aws.floci"], edge: ["api-gateway"], tests: [{ row: STREAM_ROW }] }],
  },
  {
    id: "edge-runtime-isr",
    reason: "a Next page with runtime edge and a revalidate never serves a cached tier",
    issue: 899,
    affects: [
      {
        on: ["aws.floci"],
        edge: ["api-gateway"],
        cells: ["next/web"],
        tests: [{ row: EDGE_ISR_TITLE }],
      },
    ],
  },
  {
    id: "aws-container-unimplemented",
    reason: "the aws provider advertises serverless only, so preflight refuses a container app",
    issue: 937,
    affects: [{ on: ["aws", "aws.floci"], compute: ["container"], tests: [UP_TITLE] }],
  },
  {
    id: "cloudfront-stub",
    reason: "floci's CloudFront bootstrap resources are not backed by the CloudFront API",
    issue: 852,
    affects: [
      {
        on: ["aws.floci"],
        edge: ["cloudfront"],
        cells: EVERY_AWS_CELL_BUT_LADDERS,
        tests: [UP_TITLE],
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
        edge: ["cloudflare"],
        cells: EVERY_AWS_CELL_BUT_LADDERS,
        tests: [UP_TITLE],
      },
    ],
  },
];
