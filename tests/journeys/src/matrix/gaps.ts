import {
  bindingCheck,
  bindingQueryCheck,
  clientAddressCheck,
  corsCheck,
  emptyBodyCheck,
  encodedSlashCheck,
  kvPasswordOutOfEnvironmentCheck,
  malformedQueryCheck,
  nextCacheChecks,
  nextDataCacheChecks,
  nextOriginDataCacheChecks,
  nodeRuntimeChecks,
  publicOriginCheck,
  rewrittenQueryCheck,
  sseCheck,
  sseSilenceCheck,
  streamCheck,
} from "../checks";
import { REGISTRY_TOKEN_ENV, REGISTRY_USER_ENV } from "../registry/settings";
import { check, step } from "../steps";
import { deploy, iac, kv, lifecycle, sdk } from "./fixtures";
import type { Gap } from "./types";
import {
  apiGateway,
  cloudflare,
  cloudflareInFrontOfContainers,
  cloudflareInFrontOfMixedComputes,
  cloudflareOnABox,
  cloudflareOnGoogleCloud,
  cloudflareTunnel,
  container,
  defaults,
  registry,
} from "./variants";

const DEPLOY_NEXT_BEARING = [deploy.next, deploy.workspace];
const SDK_NEXT_BEARING = [sdk.next, sdk.workspace];
const EVERY_NEXT_BEARING = [...DEPLOY_NEXT_BEARING, lifecycle.next, ...SDK_NEXT_BEARING];
const NEXT_CACHE = [...nextCacheChecks, ...nextDataCacheChecks];
const RUNTIME_NEUTRAL_DEPLOYS = [deploy.node, deploy.go, deploy.python, deploy.rust];
const TIMED_STREAMS = [streamCheck, sseCheck, sseSilenceCheck];

export const gaps: Gap[] = [
  {
    id: "rest-api-reorders-the-query",
    reason:
      "the api-gateway edge is a REST API, whose proxy event carries no raw query string and whose documentation says the order of request parameters is not preserved",
    where: [
      {
        on: ["aws", "aws.floci"],
        fixtures: RUNTIME_NEUTRAL_DEPLOYS,
        variants: [apiGateway],
        fails: [check(rewrittenQueryCheck)],
      },
    ],
  },
  {
    id: "floci-api-gateway-buffers-and-rewrites",
    reason:
      "floci's API Gateway invokes lambda synchronously and answers one buffered body, rejects a malformed escape itself, hands the path decoded, and sets no forwarded client or scheme",
    where: [
      {
        on: ["aws.floci"],
        fixtures: RUNTIME_NEUTRAL_DEPLOYS,
        variants: [apiGateway],
        fails: [
          check(TIMED_STREAMS),
          check(malformedQueryCheck),
          check(encodedSlashCheck),
          check(clientAddressCheck),
          check(publicOriginCheck),
        ],
      },
    ],
  },
  {
    id: "floci-lambda-names-no-zone",
    reason:
      "floci starts lambda with TZ=:/etc/localtime in an image that has no /etc/localtime, so node resolves no zone; lambda itself sets TZ=:UTC",
    where: [
      {
        on: ["aws.floci"],
        fixtures: [deploy.node],
        variants: [apiGateway],
        fails: [check(nodeRuntimeChecks)],
      },
    ],
  },
  {
    id: "floci-cloud-run-buffers-and-rewrites",
    reason:
      "floci-gcp reads a Cloud Run body whole before answering, rejects a malformed escape itself, answers every preflight from its storage CORS filter, and drops the Host the client asked for",
    where: [
      {
        on: ["gcp.floci"],
        fixtures: RUNTIME_NEUTRAL_DEPLOYS,
        fails: [
          check(TIMED_STREAMS),
          check(malformedQueryCheck),
          check(corsCheck),
          check(publicOriginCheck),
        ],
      },
    ],
  },
  {
    id: "dev-runs-as-development",
    reason:
      "ocel dev runs the app as development on the runner's clock, never as production in UTC",
    where: [{ on: ["dev"], fails: [check(nodeRuntimeChecks)] }],
  },
  {
    id: "no-hop-in-front-of-dev",
    reason: "ocel dev fronts the app with no proxy, so nothing appends the client address",
    where: [{ on: ["dev"], fails: [check(clientAddressCheck)] }],
  },
  {
    id: "no-router-in-front-of-dev",
    reason: "ocel dev does not front a Next app with the router, so no cache tier is observable",
    issue: 898,
    where: [
      {
        on: ["dev"],
        fixtures: [deploy.next, sdk.next],
        fails: [check(NEXT_CACHE, ["verify"])],
      },
    ],
  },
  {
    id: "cloudflare-proxies-a-box-uncached",
    reason: "Cloudflare in front of a box proxies container apps without caching them",
    issue: 1457,
    where: [
      {
        on: ["vps", "vps.incus"],
        fixtures: [lifecycle.next],
        variants: [cloudflareOnABox, cloudflareTunnel],
        fails: [check(NEXT_CACHE)],
      },
    ],
  },
  {
    id: "no-route-to-its-own-hostname",
    reason:
      "the data-cache page on a box nothing fronts fetches its app's own *.localhost hostname from inside its container, which neither resolves to the box nor trusts Caddy's local certificate",
    issue: 1458,
    where: [
      {
        on: ["vps", "vps.incus"],
        fixtures: [lifecycle.next, sdk.next],
        variants: [defaults],
        fails: [check(nextOriginDataCacheChecks)],
      },
    ],
  },
  {
    id: "sst-util-global",
    reason: "@ocel/sst reads $util off globalThis, which SST 3.19 does not set",
    issue: 857,
    where: [
      { on: ["aws", "aws.floci"], fixtures: [iac.withSst], fails: [step.deploy], skipsCell: true },
    ],
  },
  {
    id: "pulumi-provider-serialisation",
    reason: "@ocel/pulumi's dynamic provider cannot be serialised, so pulumi up fails at preview",
    issue: 856,
    where: [
      {
        on: ["aws", "aws.floci"],
        fixtures: [iac.withPulumi],
        fails: [step.deploy],
        skipsCell: true,
      },
    ],
  },
  {
    id: "migrate-needs-binding",
    reason:
      "the aws journey migrates through ocel run, which needs a console binding the lane never has",
    issue: 911,
    where: [{ on: ["aws"], fixtures: [sdk.node], fails: [step.deploy], skipsCell: true }],
  },
  {
    id: "build-needs-postgres",
    reason: "ocel build fails collecting page data for /api/todos without a resolved postgres",
    issue: 849,
    where: [
      {
        on: ["aws"],
        fixtures: [lifecycle.next, ...SDK_NEXT_BEARING],
        fails: [step.deploy],
        skipsCell: true,
      },
      {
        on: ["vps", "vps.incus"],
        fixtures: [lifecycle.next, ...SDK_NEXT_BEARING],
        variants: [defaults],
        fails: [step.deploy],
        skipsCell: true,
      },
    ],
  },
  {
    id: "cloudfront-answers-403",
    reason: "once the hostname's route resolves, every request through cloudfront answers 403",
    issue: 923,
    where: [
      {
        on: ["aws"],
        fixtures: DEPLOY_NEXT_BEARING,
        variants: [defaults],
        fails: [step.deploy],
        skipsCell: true,
      },
      { on: ["aws"], variants: [container], fails: [step.deploy], skipsCell: true },
    ],
  },
  {
    id: "api-gateway-keeps-the-sentinel",
    reason:
      "the api-gateway edge invokes lambda over a streaming invoke and drops neither the empty-body byte nor its marker",
    issue: 1145,
    where: [
      {
        on: ["aws", "aws.floci"],
        fixtures: [deploy.node, deploy.python, deploy.go, deploy.rust],
        variants: [apiGateway],
        fails: [check(emptyBodyCheck)],
      },
    ],
  },
  {
    id: "binding-query-hangs",
    reason:
      "a select through the postgres binding answers an HTML error after ~15s on a real account",
    issue: 925,
    where: [
      {
        on: ["aws"],
        fixtures: [sdk.withTransforms],
        variants: [apiGateway],
        fails: [check(bindingQueryCheck)],
      },
    ],
  },
  {
    id: "stale-release-after-deploy",
    reason:
      "the first request after ocel deploy still answers with the previous release's environment",
    issue: 926,
    where: [
      {
        on: ["aws"],
        fixtures: [sdk.withTransforms],
        variants: [apiGateway],
        fails: [check(bindingCheck, ["redeploy"])],
      },
    ],
  },
  {
    id: "no-master-secret",
    reason:
      "an RDS cluster with ManageMasterUserPassword reports no master user secret under floci",
    issue: 884,
    where: [
      {
        on: ["aws.floci"],
        fixtures: [sdk.node, sdk.withTransforms],
        variants: [apiGateway],
        fails: [step.deploy],
        skipsCell: true,
      },
    ],
  },
  {
    id: "floci-runs-no-load-balancer",
    reason:
      "the aws provider runs a container on Fargate behind a load balancer floci has no data plane for",
    issue: 995,
    where: [{ on: ["aws.floci"], variants: [container], fails: [step.deploy], skipsCell: true }],
  },
  {
    id: "next-on-cloud-run",
    reason:
      "gcp phase 3 serves node, go, python and rust; a Next app has no router in front of it there",
    issue: 1097,
    where: [
      {
        on: ["gcp", "gcp.floci"],
        fixtures: DEPLOY_NEXT_BEARING,
        fails: [step.deploy],
        skipsCell: true,
      },
    ],
  },
  {
    id: "cloudfront-stub",
    reason: "floci's CloudFront bootstrap resources are not backed by the CloudFront API",
    issue: 852,
    where: [
      {
        on: ["aws.floci"],
        fixtures: EVERY_NEXT_BEARING,
        variants: [defaults],
        fails: [step.deploy],
        skipsCell: true,
      },
    ],
  },
  {
    id: "no-registry-credentials",
    reason:
      "only a run handed the user and token that may push to the journey registry can deploy through it",
    where: [
      {
        on: ["vps", "vps.incus"],
        variants: [registry],
        whileUnset: [REGISTRY_USER_ENV, REGISTRY_TOKEN_ENV],
        fails: [step.deploy],
        skipsCell: true,
      },
    ],
  },
  {
    id: "no-cloudflare-api",
    reason: "nothing emulates the Cloudflare API under floci",
    issue: 904,
    where: [
      {
        on: ["aws.floci"],
        fixtures: EVERY_NEXT_BEARING,
        variants: [cloudflare],
        fails: [step.deploy],
        skipsCell: true,
      },
      {
        on: ["gcp.floci"],
        variants: [cloudflareOnGoogleCloud],
        fails: [step.deploy],
        skipsCell: true,
      },
      {
        on: ["aws.floci"],
        variants: [cloudflareInFrontOfContainers, cloudflareInFrontOfMixedComputes],
        fails: [step.deploy],
        skipsCell: true,
      },
    ],
  },
  {
    id: "cloudflare-cannot-reach-the-vm",
    reason:
      "Cloudflare forwards to a public origin, and an incus VM is reachable only from the machine it runs on",
    where: [
      { on: ["vps.incus"], variants: [cloudflareOnABox], fails: [step.deploy], skipsCell: true },
    ],
  },
  {
    id: "cloudflare-tunnel-needs-a-real-box",
    reason:
      "a tunnel cell asks the box's own address for the hostname, and deploys into the E2E Cloudflare account, which only the nightly real-VPS lane reaches",
    where: [
      { on: ["vps.incus"], variants: [cloudflareTunnel], fails: [step.deploy], skipsCell: true },
    ],
  },
  {
    id: "no-cloudflare-zone",
    reason:
      "a cell Cloudflare fronts is answered on a hostname in a zone the run's Cloudflare token can write, and only a run that names both deploys it",
    where: [
      {
        on: ["vps", "gcp"],
        variants: [cloudflareOnABox, cloudflareOnGoogleCloud],
        whileUnset: ["OCEL_JOURNEY_ZONE", "CLOUDFLARE_API_TOKEN", "CLOUDFLARE_ACCOUNT_ID"],
        fails: [step.deploy],
        skipsCell: true,
      },
      {
        on: ["vps"],
        variants: [cloudflareTunnel],
        whileUnset: ["OCEL_JOURNEY_ZONE", "CLOUDFLARE_API_TOKEN", "CLOUDFLARE_ACCOUNT_ID"],
        fails: [step.deploy],
        skipsCell: true,
      },
      {
        on: ["aws"],
        variants: [cloudflareInFrontOfContainers, cloudflareInFrontOfMixedComputes],
        whileUnset: ["OCEL_JOURNEY_ZONE", "CLOUDFLARE_API_TOKEN", "CLOUDFLARE_ACCOUNT_ID"],
        fails: [step.deploy],
        skipsCell: true,
      },
    ],
  },
  {
    id: "dev-binds-through-the-environment",
    reason:
      "ocel dev hands each binding to the app it runs as an environment variable, a kv store's password in clear among it",
    issue: 1522,
    where: [{ on: ["dev"], fixtures: [kv.node], fails: [check(kvPasswordOutOfEnvironmentCheck)] }],
  },
  {
    id: "gcp-refuses-kv",
    reason:
      "the gcp provider refuses a deploy that declares a store at preflight: kv stores are unsupported on gcp, so it provisions none",
    issue: 1480,
    where: [
      {
        on: ["gcp", "gcp.floci"],
        fixtures: [kv.node],
        fails: [step.deploy],
        skipsCell: true,
      },
    ],
  },
];
