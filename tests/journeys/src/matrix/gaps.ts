import {
  bindingCheck,
  bindingQueryCheck,
  emptyBodyCheck,
  nextCacheChecks,
  nextDataCacheChecks,
} from "../checks";
import { check, step } from "../steps";
import { deploy, lifecycle, sdk } from "./fixtures";
import type { Gap } from "./types";
import { apiGateway, cloudflare, container, defaults } from "./variants";

const DEPLOY_NEXT_BEARING = [deploy.next, deploy.workspace];
const SDK_NEXT_BEARING = [sdk.next, sdk.workspace];
const EVERY_NEXT_BEARING = [...DEPLOY_NEXT_BEARING, lifecycle.next, ...SDK_NEXT_BEARING];
const NEXT_CACHE = [...nextCacheChecks, ...nextDataCacheChecks];

export const gaps: Gap[] = [
  {
    id: "no-router-in-front-of-dev",
    reason: "ocel dev does not front a Next app with the router, so no cache tier is observable",
    issue: 898,
    where: [
      {
        on: ["dev", "dev-local"],
        fixtures: [deploy.next, sdk.next],
        fails: [check(NEXT_CACHE, ["verify"])],
      },
    ],
  },
  {
    id: "no-bucket-on-a-box",
    reason: "the vps provider serves no bucket, so every sdk fixture is refused at deploy",
    issue: 918,
    where: [
      {
        on: ["vps", "vps.incus"],
        fixtures: [lifecycle.next, sdk.node, sdk.next, sdk.workspace],
        variants: [defaults],
        fails: [step.deploy],
        skipsCell: true,
      },
    ],
  },
  {
    id: "no-router-on-a-box",
    reason: "vps serves a Next app from a container behind Caddy, with no cache router in front",
    issue: 900,
    where: [
      {
        on: ["vps", "vps.incus"],
        fixtures: [deploy.next, sdk.next, lifecycle.next],
        variants: [defaults],
        fails: [check(NEXT_CACHE)],
      },
    ],
  },
  {
    id: "sst-util-global",
    reason: "@ocel/sst reads $util off globalThis, which SST 3.19 does not set",
    issue: 857,
    where: [
      { on: ["aws", "aws.floci"], fixtures: [sdk.withSst], fails: [step.deploy], skipsCell: true },
    ],
  },
  {
    id: "pulumi-provider-serialisation",
    reason: "@ocel/pulumi's dynamic provider cannot be serialised, so pulumi up fails at preview",
    issue: 856,
    where: [
      {
        on: ["aws", "aws.floci"],
        fixtures: [sdk.withPulumi],
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
    id: "no-cloudflare-api",
    reason: "nothing stands in for the Cloudflare API under floci",
    issue: 904,
    where: [
      {
        on: ["aws.floci"],
        fixtures: EVERY_NEXT_BEARING,
        variants: [cloudflare],
        fails: [step.deploy],
        skipsCell: true,
      },
    ],
  },
];
