import {
  batchCheck,
  bindingCheck,
  bindingQueryCheck,
  clientAddressCheck,
  corsCheck,
  emptyBodyCheck,
  encodedSlashCheck,
  exactTaskPayloadCheck,
  exactTopicPayloadCheck,
  hyphenatedTaskCheck,
  kvPasswordOutOfEnvironmentCheck,
  lanesCheck,
  malformedQueryCheck,
  nextCacheChecks,
  nextDataCacheChecks,
  nextOriginDataCacheChecks,
  nodeRuntimeChecks,
  orderedKeysInParallelCheck,
  publicOriginCheck,
  realtimeAppOriginCheck,
  realtimeConnectTokenVectorsCheck,
  realtimeEventSizeCheck,
  realtimeHandlerDefaultsCheck,
  realtimePublicCheck,
  realtimeReauthorizeCheck,
  realtimeRelayedPublishCheck,
  realtimeRuleAllowsCheck,
  realtimeSchemaCheck,
  realtimeSubscribeTokenVectorsCheck,
  realtimeSubscriptionLimitCheck,
  realtimeWildcardCheck,
  rewrittenQueryCheck,
  sseCheck,
  sseSilenceCheck,
  streamCheck,
  taskConcurrencyCheck,
  todoAndDocumentChecks,
} from "../checks";
import { REGISTRY_TOKEN_ENV, REGISTRY_USER_ENV } from "../registry/settings";
import { check, step } from "../steps";
import { deploy, iac, kv, lifecycle, realtime, sdk, tasks } from "./fixtures";
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
        fixtures: [...RUNTIME_NEUTRAL_DEPLOYS, sdk.node],
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
        fixtures: [...RUNTIME_NEUTRAL_DEPLOYS, sdk.node],
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
        fixtures: [deploy.node, sdk.node],
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
      {
        on: ["gcp.floci"],
        fixtures: Object.values(realtime),
        fails: [check(realtimeHandlerDefaultsCheck)],
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
      "the aws journey migrates through ocel run, which migrates the local dev database rather than the deployed one",
    issue: 911,
    where: [
      { on: ["aws"], fixtures: [sdk.node], fails: [step.deploy], skipsCell: true },
      {
        on: ["aws.floci"],
        fixtures: [sdk.node],
        variants: [apiGateway],
        fails: [check(todoAndDocumentChecks)],
      },
    ],
  },
  {
    id: "destroy-keeps-a-full-bucket",
    reason:
      "ocel destroy deletes an app bucket without emptying it, and S3 refuses to delete a bucket that holds an object",
    issue: 1712,
    where: [
      {
        on: ["aws.floci"],
        fixtures: [sdk.node],
        variants: [apiGateway],
        fails: [step.destroy],
      },
    ],
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
        fixtures: [deploy.node, deploy.python, deploy.go, deploy.rust, sdk.node],
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
    id: "floci-runs-no-load-balancer",
    reason:
      "the aws provider runs a container on Fargate behind a load balancer floci has no data plane for",
    issue: 995,
    where: [{ on: ["aws.floci"], variants: [container], fails: [step.deploy], skipsCell: true }],
  },
  {
    id: "floci-runs-no-event-api",
    reason:
      "the aws provider serves realtime from an AppSync Event API, and floci emulates AppSync's GraphQL APIs alone: no CreateApi, Lambda authorizer, POST /event or Event socket",
    issue: 1570,
    where: [
      {
        on: ["aws.floci"],
        fixtures: Object.values(realtime),
        fails: [step.deploy],
        skipsCell: true,
      },
    ],
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
        whileUnset: ["OCEL_E2E_ZONE", "CLOUDFLARE_API_TOKEN", "CLOUDFLARE_ACCOUNT_ID"],
        fails: [step.deploy],
        skipsCell: true,
      },
      {
        on: ["vps"],
        variants: [cloudflareTunnel],
        whileUnset: ["OCEL_E2E_ZONE", "CLOUDFLARE_API_TOKEN", "CLOUDFLARE_ACCOUNT_ID"],
        fails: [step.deploy],
        skipsCell: true,
      },
      {
        on: ["aws"],
        variants: [cloudflareInFrontOfContainers, cloudflareInFrontOfMixedComputes],
        whileUnset: ["OCEL_E2E_ZONE", "CLOUDFLARE_API_TOKEN", "CLOUDFLARE_ACCOUNT_ID"],
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
    id: "floci-serves-no-memorystore",
    reason:
      "floci serves no Memorystore, and the floci lane's bootstrap installs no kv network, so the gcp provider refuses a deploy that declares a store at preflight",
    where: [
      {
        on: ["gcp.floci"],
        fixtures: [kv.node],
        fails: [step.deploy],
        skipsCell: true,
      },
    ],
  },
  {
    id: "hyphenated-binding-key-dropped-by-sh",
    reason:
      "a binding's env key holds the declared name, hyphen and all, and the sh that runs the app's dev script drops a variable whose name holds a hyphen",
    issue: 1526,
    where: [{ on: ["dev"], fixtures: [tasks.node], fails: [check(hyphenatedTaskCheck)] }],
  },
  {
    id: "typescript-sdk-parses-payloads",
    reason:
      "the TypeScript SDK parses an envelope and a run record with JSON.parse, so a handler and runs.retrieve get 2.0 as 2 and an integer past 2^53 rounded, though the request it sent and the envelope it was delivered carry the exact text",
    issue: 1528,
    where: [
      {
        on: ["dev", "vps", "vps.incus", "aws", "aws.floci", "gcp", "gcp.floci"],
        fixtures: [tasks.node],
        fails: [check(exactTaskPayloadCheck), check(exactTopicPayloadCheck)],
      },
    ],
  },
  {
    id: "go-image-built-from-the-app-path-alone",
    reason:
      "a container app's image is built from its path alone, and the Go fixtures' module is rooted above ./server and replaces ocel.dev with the repository's sdk, so go build in the image finds no go.mod",
    issue: 1550,
    where: [
      {
        on: ["vps", "vps.incus"],
        fixtures: [tasks.go, realtime.go],
        fails: [step.deploy],
        skipsCell: true,
      },
    ],
  },
  {
    id: "path-dependency-outside-the-image",
    reason:
      "a container app's image is built from its path alone, and the Python and Rust fixtures depend on the repository's SDK by a path outside it, so the install in the image finds no SDK",
    issue: 1598,
    where: [
      {
        on: ["vps", "vps.incus"],
        fixtures: [realtime.python, realtime.rust],
        fails: [step.deploy],
        skipsCell: true,
      },
    ],
  },
  {
    id: "python-function-vendors-requirements-alone",
    reason:
      "a Python function vendors what requirements.txt lists and nothing else, and realtime/python declares its dependencies, the SDK among them, in pyproject.toml, so the function cannot import them and Cloud Run never sees it ready",
    issue: 1260,
    where: [
      {
        on: ["gcp", "gcp.floci"],
        fixtures: [realtime.python],
        fails: [step.deploy],
        skipsCell: true,
      },
    ],
  },
  {
    id: "rust-attribution-resolves-offline",
    reason:
      "attribution resolves a rust app's whole lockfile with cargo metadata --offline, and the build downloaded only what its musl target builds, so a crate only another platform builds is missing and the manifest is never assembled",
    issue: 1609,
    where: [
      {
        on: ["gcp", "gcp.floci"],
        fixtures: [realtime.rust],
        fails: [step.deploy],
        skipsCell: true,
      },
    ],
  },
  {
    id: "aws-reads-lanes-unweighted",
    reason:
      "on aws each queue is read by its own event source mapping in the order SQS hands its messages out, and SQS has no priority, so a lane is recorded on the run but takes no share of the reads",
    issue: 1559,
    where: [{ on: ["aws", "aws.floci"], fixtures: [tasks.node], fails: [check(lanesCheck)] }],
  },
  {
    id: "floci-polls-one-batch-per-mapping",
    reason:
      "floci's SQS event source poller holds one poll per mapping and waits out the invocation it starts, so a queue is delivered one batch at a time and no two runs of one task overlap, where AWS scales its pollers up to the mapping's maximum concurrency",
    issue: 1558,
    where: [
      {
        on: ["aws.floci"],
        fixtures: [tasks.node],
        fails: [check(orderedKeysInParallelCheck), check(taskConcurrencyCheck)],
      },
    ],
  },
  {
    id: "gcp-delivers-lanes-unweighted",
    reason:
      "a gcp consumer is one Pub/Sub push subscription, and Pub/Sub has no priority, so a run's lane is recorded and takes no share of the deliveries",
    issue: 1562,
    where: [{ on: ["gcp", "gcp.floci"], fixtures: [tasks.node], fails: [check(lanesCheck)] }],
  },
  {
    id: "gcp-pushes-batches-of-one",
    reason:
      "a gcp consumer is a Pub/Sub push subscription, which pushes one message per request, so a batch consumer is handed batches of one",
    issue: 1563,
    where: [{ on: ["gcp", "gcp.floci"], fixtures: [tasks.node], fails: [check(batchCheck)] }],
  },
  {
    id: "gcp-runs-tasks-unbounded",
    reason:
      "a gcp worker bounds its runs by its Cloud Run concurrency alone, and nothing holds one task's runs below it, so a task's own concurrency is not enforced",
    issue: 1564,
    where: [
      { on: ["gcp", "gcp.floci"], fixtures: [tasks.node], fails: [check(taskConcurrencyCheck)] },
    ],
  },
  {
    id: "floci-runs-no-gateway-socket-or-publish",
    reason:
      "floci-gcp proxies a Cloud Run service by host with no WebSocket upgrade and no secret volume, so the realtime gateway it runs holds no socket and the runtime's publish to it is never heard",
    issue: 1605,
    where: [
      {
        on: ["gcp.floci"],
        fixtures: Object.values(realtime),
        fails: [
          check([
            realtimeRuleAllowsCheck,
            realtimeAppOriginCheck,
            realtimePublicCheck,
            realtimeWildcardCheck,
            realtimeConnectTokenVectorsCheck,
            realtimeSubscribeTokenVectorsCheck,
            realtimeRelayedPublishCheck,
            realtimeSchemaCheck,
            realtimeReauthorizeCheck,
            realtimeSubscriptionLimitCheck,
            realtimeEventSizeCheck,
          ]),
        ],
      },
    ],
  },
];
