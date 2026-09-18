import {
  bindingChecks,
  envChecks,
  healthChecks,
  httpProbeChecks,
  nativeModuleChecks,
  nextCacheChecks,
  nextDataCacheChecks,
  nextRoutingChecks,
  nextStateChecks,
  staticChecks,
  todoAndDocumentChecks,
  vendoredDependencyChecks,
} from "../checks";
import { pulumiLadder } from "../targets/aws/ladder-pulumi";
import { sstLadder } from "../targets/aws/ladder-sst";
import { type Fixture, fixture } from "./types";
import { apiGateway, cloudflare, container } from "./variants";

const RUNTIME_NEUTRAL_CHECKS = [...healthChecks, ...staticChecks, ...httpProbeChecks];
const NODE_CHECKS = [...RUNTIME_NEUTRAL_CHECKS, ...nativeModuleChecks];
const NODE_SDK_CHECKS = [
  ...healthChecks,
  ...staticChecks,
  ...todoAndDocumentChecks,
  ...nativeModuleChecks,
  ...httpProbeChecks,
  ...envChecks,
];
const NEXT_ROUTING_AND_CACHE_CHECKS = [...nextRoutingChecks, ...nextCacheChecks];
const NEXT_STATE_AND_DATA_CACHE_CHECKS = [...nextStateChecks, ...nextDataCacheChecks];
const BINDING_CHECKS = [...healthChecks, ...staticChecks, ...bindingChecks];

export const deploy = {
  node: fixture("deploy/node", {
    apps: ["web"],
    checks: NODE_CHECKS,
    on: {
      dev: { base: true },
      "dev-local": { base: true },
      aws: { variants: [container, apiGateway] },
      vps: { base: true },
      gcp: { base: true, variants: [container] },
    },
    sample: { group: "node-http" },
  }),
  go: fixture("deploy/go", {
    apps: ["web"],
    checks: RUNTIME_NEUTRAL_CHECKS,
    on: {
      aws: { variants: [container, apiGateway] },
      vps: { base: true },
      gcp: { base: true, variants: [container] },
    },
  }),
  python: fixture("deploy/python", {
    apps: ["web"],
    checks: [...RUNTIME_NEUTRAL_CHECKS, ...vendoredDependencyChecks],
    on: {
      aws: { variants: [container, apiGateway] },
      vps: { base: true },
      gcp: { base: true, variants: [container] },
    },
  }),
  rust: fixture("deploy/rust", {
    apps: ["web"],
    checks: RUNTIME_NEUTRAL_CHECKS,
    on: {
      aws: { variants: [container, apiGateway] },
      vps: { base: true },
      gcp: { base: true, variants: [container] },
    },
  }),
  next: fixture("deploy/next", {
    apps: ["web"],
    checks: [...NODE_CHECKS, ...NEXT_ROUTING_AND_CACHE_CHECKS],
    on: {
      dev: { base: true },
      "dev-local": { base: true },
      aws: { base: true, variants: [container, cloudflare] },
      vps: { base: true },
      gcp: { base: true, variants: [container] },
    },
  }),
  workspace: fixture("deploy/workspace", {
    apps: ["next", "express"],
    checks: NODE_CHECKS,
    on: {
      dev: { base: true },
      "dev-local": { base: true },
      aws: { base: true, variants: [container, cloudflare] },
      vps: { base: true },
      gcp: { base: true, variants: [container] },
    },
    sample: { group: "node-http", lead: true },
  }),
};

export const lifecycle = {
  next: fixture("lifecycle/next", {
    apps: ["web"],
    redeploys: true,
    checks: [
      ...NODE_SDK_CHECKS,
      ...NEXT_ROUTING_AND_CACHE_CHECKS,
      ...NEXT_STATE_AND_DATA_CACHE_CHECKS,
    ],
    on: {
      aws: { base: true, variants: [container, cloudflare] },
      vps: { base: true },
    },
  }),
};

export const sdk = {
  node: fixture("sdk/node", {
    apps: ["web"],
    checks: NODE_SDK_CHECKS,
    on: {
      dev: { base: true },
      "dev-local": { base: true },
      aws: { variants: [container, apiGateway] },
      vps: { base: true },
    },
    sample: { group: "node-http" },
  }),
  next: fixture("sdk/next", {
    apps: ["web"],
    checks: [
      ...NODE_SDK_CHECKS,
      ...NEXT_ROUTING_AND_CACHE_CHECKS,
      ...NEXT_STATE_AND_DATA_CACHE_CHECKS,
    ],
    on: {
      dev: { base: true },
      "dev-local": { base: true },
      aws: { base: true, variants: [container, cloudflare] },
      vps: { base: true },
    },
  }),
  workspace: fixture("sdk/workspace", {
    apps: ["next", "express"],
    checks: NODE_SDK_CHECKS,
    on: {
      dev: { base: true },
      "dev-local": { base: true },
      aws: { base: true, variants: [container, cloudflare] },
      vps: { base: true },
    },
    sample: { group: "node-http", lead: true },
  }),
  withTransforms: fixture("sdk/with-transforms", {
    apps: ["web"],
    redeploys: true,
    checks: BINDING_CHECKS,
    on: { aws: { variants: [container, apiGateway] } },
  }),
  withSst: fixture("sdk/with-sst", {
    apps: ["web"],
    redeploys: true,
    checks: BINDING_CHECKS,
    ladder: sstLadder,
    on: { aws: { variants: [container, apiGateway] } },
  }),
  withPulumi: fixture("sdk/with-pulumi", {
    apps: ["web"],
    redeploys: true,
    checks: BINDING_CHECKS,
    ladder: pulumiLadder,
    on: { aws: { variants: [container, apiGateway] } },
  }),
};

export const fixtures: Fixture[] = [
  ...Object.values(deploy),
  ...Object.values(lifecycle),
  ...Object.values(sdk),
];
