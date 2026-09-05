import { EDGE_ISR_TITLE } from "../nextCache";
import { DESTROY_TITLE, UP_TITLE } from "../plan";
import type { Gap } from "./types";

const NODE_HTTP = ["express/web", "hono/web", "fastify/web"];
const COMPOSITE = [...NODE_HTTP, "next/web"];
const WORKSPACE = ["workspace/next", "workspace/express"];
const EVERY_AWS_CELL_BUT_LADDERS = [...COMPOSITE, ...WORKSPACE, "with-transforms/web"];

const BASE = ["base"];
const HELLO = ["hello"];
const SERVERLESS = ["base", "api-gateway", "cloudflare"];
const ON_API_GATEWAY = ["api-gateway", "hello-api-gateway"];

const UPLOAD_ROW = "the upload protocol stores a document and /api/documents lists it";
const STREAM_ROW = "GET /api/probes/stream streams its chunks in order to the sentinel";
const LINK_QUERY_ROW = "GET /api/link/query answers ok after a select through the link";
const LINK_ROW = "GET /api/link answers with what it resolved and the greeting it deployed with";

export const gaps: Gap[] = [
  {
    id: "env-set-needs-provider",
    reason: "ocel env set demands a provider, so dev cannot deliver GREETING or SECRET_TOKEN",
    issue: 881,
    affects: [{ on: ["dev"], tests: [UP_TITLE], skip: true }],
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
    affects: [
      {
        on: ["vps", "vps.incus"],
        cells: [...COMPOSITE, ...WORKSPACE],
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
        cells: ["next/web"],
        variants: BASE,
        tests: [{ rows: ["next-cache"] }],
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
        cells: ["with-sst/web"],
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
        cells: ["with-pulumi/web"],
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
      { on: ["aws"], cells: NODE_HTTP, variants: SERVERLESS, tests: [UP_TITLE], skip: true },
    ],
  },
  {
    id: "build-needs-postgres",
    reason: "ocel build fails collecting page data for /api/todos without a resolved postgres",
    issue: 849,
    affects: [
      {
        on: ["aws"],
        cells: ["next/web", ...WORKSPACE],
        variants: SERVERLESS,
        tests: [UP_TITLE],
        skip: true,
      },
      {
        on: ["aws.floci"],
        cells: ["next/web", ...WORKSPACE],
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
        cells: ["next/web", ...WORKSPACE],
        variants: ["hello-api-gateway"],
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
        cells: [...COMPOSITE, ...WORKSPACE],
        variants: HELLO,
        tests: [UP_TITLE],
        skip: true,
      },
      {
        on: ["aws"],
        cells: ["with-transforms/web"],
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
        cells: ["with-transforms/web"],
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
        cells: ["with-transforms/web"],
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
        cells: ["with-transforms/web"],
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
        cells: [...NODE_HTTP, "with-transforms/web"],
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
        variants: ON_API_GATEWAY,
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
        cells: ["next/web"],
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
        variants: [...BASE, ...HELLO],
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
