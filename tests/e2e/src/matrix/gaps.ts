import {
  authenticateHeaderCheck,
  batchCheck,
  bindingCheck,
  clientAddressCheck,
  clientEnvDeploymentUrlCheck,
  corsCheck,
  emptyBodyCheck,
  encodedSlashCheck,
  immutableChunkCheck,
  lanesCheck,
  malformedQueryCheck,
  missingImmutableChunkCheck,
  missingNextAssetCheck,
  nextCacheChecks,
  nextDataCacheChecks,
  nextOriginDataCacheChecks,
  nodeRuntimeChecks,
  orderedKeysInParallelCheck,
  publicOriginCheck,
  publicOriginFormActionCheck,
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
  runtimeStampCheck,
  sseCheck,
  sseSilenceCheck,
  streamCheck,
  streamPageCheck,
  taskConcurrencyCheck,
  todoAndDocumentChecks,
} from "../checks";
import { REGISTRY_TOKEN_ENV, REGISTRY_USER_ENV } from "../registry/settings";
import { check, step } from "../steps";
import { deploy, lifecycle, sdk } from "./fixtures";
import type { Gap } from "./types";
import {
  alb,
  albContainerDockerfile,
  albInFrontOfNextContainer,
  apiGateway,
  cloudflare,
  cloudflareInFrontOfContainers,
  cloudflareInFrontOfMixedComputes,
  cloudflareOnABox,
  cloudflareOnGoogleCloud,
  cloudflareTunnel,
  cloudfrontShielded,
  container,
  containerDockerfile,
  defaults,
  dockerfile,
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
    id: "floci-unzips-symlinks-as-files",
    reason:
      "floci unpacks a symlink in a function's zip as a file holding the link's target, so a function traced from a pnpm install finds none of the packages it reaches through one, and never boots",
    where: [
      {
        on: ["aws.floci"],
        fixtures: [deploy.sveltekit],
        variants: [apiGateway],
        fails: [check(deploy.sveltekit.checks.filter((one) => one !== missingImmutableChunkCheck))],
      },
    ],
  },
  {
    id: "floci-api-gateway-serves-no-s3",
    reason:
      "floci's API Gateway answers an S3 service integration as a call to its own query API, so the static route serves its MissingAction error with a 200 for every content-hashed file",
    where: [
      {
        on: ["aws.floci"],
        fixtures: [deploy.sveltekit],
        variants: [apiGateway],
        fails: [check([immutableChunkCheck, missingImmutableChunkCheck])],
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
        fixtures: DEPLOY_NEXT_BEARING,
        fails: [
          check(TIMED_STREAMS),
          check(malformedQueryCheck),
          check(corsCheck),
          check(publicOriginCheck),
        ],
      },
      {
        on: ["gcp.floci"],
        fixtures: [deploy.next],
        fails: [check(streamPageCheck)],
      },
      {
        on: ["gcp.floci"],
        fixtures: [deploy.sveltekit],
        fails: [check(publicOriginFormActionCheck)],
      },
      {
        on: ["gcp.floci"],
        fixtures: [sdk.node, sdk.go, sdk.python, sdk.rust],
        fails: [check(realtimeHandlerDefaultsCheck)],
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
        variants: [defaults, dockerfile],
        fails: [check(nextOriginDataCacheChecks)],
      },
    ],
  },
  {
    id: "floci-serves-no-session-manager",
    reason:
      "aws forwards a deploy's bindings over Session Manager, which floci does not serve, so lifecycle.preBuild cannot migrate the deployed database and the tables the todo and document checks write are never created",
    issue: 1878,
    where: [
      {
        on: ["aws.floci"],
        fixtures: [sdk.node],
        variants: [apiGateway],
        fails: [check(todoAndDocumentChecks)],
      },
    ],
  },
  {
    id: "gcp-postgres-is-private-to-the-runner",
    reason:
      "a gcp postgres is a Cloud SQL instance with no public address, reached over the tier's private network alone, so the journey cannot migrate it from the runner and the tables the todo and document checks write are never created",
    where: [{ on: ["gcp"], fixtures: [sdk.node], fails: [check(todoAndDocumentChecks)] }],
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
    id: "cloudfront-runs-no-function-on-an-error",
    reason:
      "CloudFront runs no viewer-response function on a status of 400 or more, so the edge cannot rename the x-amzn-remapped-www-authenticate a function URL sends on a 401 back to WWW-Authenticate, nor answer a missing static asset with plain text instead of S3's XML 404; a custom error response would replace the app's own error pages across the distribution",
    where: [
      {
        on: ["aws"],
        fixtures: [deploy.next],
        variants: [defaults],
        fails: [check([authenticateHeaderCheck, missingNextAssetCheck])],
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
        fixtures: [sdk.node],
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
    where: [
      {
        on: ["aws.floci"],
        variants: [container, containerDockerfile],
        fails: [step.deploy],
        skipsCell: true,
      },
    ],
  },
  {
    id: "floci-runs-no-event-api",
    reason:
      "the aws provider serves realtime from an AppSync Event API, and floci emulates AppSync's GraphQL APIs alone: no CreateApi, Lambda authorizer, POST /event or Event socket",
    issue: 1570,
    where: [
      {
        on: ["aws.floci"],
        fixtures: [sdk.node, sdk.go, sdk.python, sdk.rust],
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
        fixtures: [...EVERY_NEXT_BEARING, deploy.nextPpr, deploy.sveltekit, sdk.sveltekit],
        variants: [defaults, cloudfrontShielded],
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
        variants: [cloudflare, cloudflareInFrontOfContainers, cloudflareInFrontOfMixedComputes],
        fails: [step.deploy],
        skipsCell: true,
      },
      {
        on: ["gcp.floci"],
        variants: [cloudflareOnGoogleCloud, alb, albInFrontOfNextContainer, albContainerDockerfile],
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
      "a cell Cloudflare fronts, or whose hostnames' records and certificate validation ocel writes in Cloudflare, is answered on a hostname in a zone the run's Cloudflare token can write, and only a run that names both deploys it",
    where: [
      {
        on: ["vps", "gcp"],
        variants: [cloudflareOnABox, cloudflareOnGoogleCloud],
        whileUnset: ["OCEL_E2E_ZONE", "CLOUDFLARE_API_TOKEN", "CLOUDFLARE_ACCOUNT_ID"],
        fails: [step.deploy],
        skipsCell: true,
      },
      {
        on: ["gcp"],
        variants: [alb, albInFrontOfNextContainer, albContainerDockerfile],
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
    id: "google-cloud-origins-serve-no-edge-runtime",
    reason:
      "a Google Cloud origin serves no edge runtime, so a route that declares it runs on node and stamps itself nodejs, where ocel's Next adapter built the app",
    where: [
      {
        on: ["gcp", "gcp.floci"],
        fixtures: [deploy.next, lifecycle.next, sdk.next],
        variants: [defaults, container, alb, albInFrontOfNextContainer, cloudflareOnGoogleCloud],
        fails: [check(runtimeStampCheck)],
      },
    ],
  },
  {
    id: "cloud-run-hands-no-deployment-url",
    reason:
      "a gcp cell nothing fronts is served on the url Cloud Run gives its service and declares no hostname, and ocel writes the deployment url from the hostname an app is served on",
    where: [
      {
        on: ["gcp", "gcp.floci"],
        fixtures: [sdk.next, sdk.sveltekit],
        variants: [defaults],
        fails: [check(clientEnvDeploymentUrlCheck)],
      },
    ],
  },
  {
    id: "floci-serves-no-memorystore",
    reason:
      "floci serves no Memorystore, and the floci lane's bootstrap installs no private network, so the gcp provider refuses a deploy that declares a store at preflight",
    where: [
      {
        on: ["gcp.floci"],
        fixtures: [sdk.node],
        fails: [step.deploy],
        skipsCell: true,
      },
    ],
  },
  {
    id: "floci-serves-no-cloud-sql",
    reason:
      "floci serves no Cloud SQL, and the floci lane's bootstrap installs no private network, so the gcp provider refuses a deploy that declares a postgres at preflight",
    where: [
      {
        on: ["gcp.floci"],
        fixtures: [lifecycle.next, ...SDK_NEXT_BEARING, sdk.node, sdk.go, sdk.python, sdk.rust],
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
        fixtures: [sdk.python, sdk.rust],
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
        fixtures: [sdk.rust],
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
    where: [{ on: ["aws", "aws.floci"], fixtures: [sdk.node], fails: [check(lanesCheck)] }],
  },
  {
    id: "floci-polls-one-batch-per-mapping",
    reason:
      "floci's SQS event source poller holds one poll per mapping and waits out the invocation it starts, so a queue is delivered one batch at a time and no two runs of one task overlap, where AWS scales its pollers up to the mapping's maximum concurrency",
    issue: 1558,
    where: [
      {
        on: ["aws.floci"],
        fixtures: [sdk.node],
        fails: [check(orderedKeysInParallelCheck), check(taskConcurrencyCheck)],
      },
    ],
  },
  {
    id: "gcp-delivers-lanes-unweighted",
    reason:
      "a gcp consumer is one Pub/Sub push subscription, and Pub/Sub has no priority, so a run's lane is recorded and takes no share of the deliveries",
    issue: 1562,
    where: [{ on: ["gcp", "gcp.floci"], fixtures: [sdk.node], fails: [check(lanesCheck)] }],
  },
  {
    id: "gcp-pushes-batches-of-one",
    reason:
      "a gcp consumer is a Pub/Sub push subscription, which pushes one message per request, so a batch consumer is handed batches of one",
    issue: 1563,
    where: [{ on: ["gcp", "gcp.floci"], fixtures: [sdk.node], fails: [check(batchCheck)] }],
  },
  {
    id: "gcp-runs-tasks-unbounded",
    reason:
      "a gcp worker bounds its runs by its Cloud Run concurrency alone, and nothing holds one task's runs below it, so a task's own concurrency is not enforced",
    issue: 1564,
    where: [
      { on: ["gcp", "gcp.floci"], fixtures: [sdk.node], fails: [check(taskConcurrencyCheck)] },
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
        fixtures: [sdk.node, sdk.go, sdk.python, sdk.rust],
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
