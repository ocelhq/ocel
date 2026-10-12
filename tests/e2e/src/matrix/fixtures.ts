import {
  bindingChecks,
  buildVariablesChecks,
  envChecks,
  failingPreBuildRefusal,
  healthChecks,
  httpProbeChecks,
  kvChecks,
  nativeModuleChecks,
  nextCacheChecks,
  nextClientEnvChecks,
  nextDataCacheChecks,
  nextOriginCacheChecks,
  nextOriginDataCacheChecks,
  nextPprChecks,
  nextRoutingChecks,
  nextStateChecks,
  nextStoreReadChecks,
  nodeRuntimeChecks,
  preBuildChecks,
  preBuildPrerenderChecks,
  prerenderBucketChecks,
  prerenderChecks,
  realtimeChecks,
  staticChecks,
  svelteKitChecks,
  svelteKitClientEnvChecks,
  tasksChecks,
  tasksWireChecks,
  todoAndDocumentChecks,
  vendoredDependencyChecks,
} from "../checks";
import { PulumiStack } from "../targets/aws/stacks/pulumi";
import { SstStack } from "../targets/aws/stacks/sst";
import { awsWorld } from "../targets/aws/world";
import { type Fixture, fixture } from "./types";
import {
  alb,
  albInFrontOfNext,
  apiGateway,
  cloudflare,
  cloudflareInFrontOfContainers,
  cloudflareInFrontOfMixedComputes,
  cloudflareOnABox,
  cloudflareOnGoogleCloud,
  cloudflareTunnel,
  cloudfrontShielded,
  container,
  defaults,
  dockerfile,
  registry,
} from "./variants";

const RUNTIME_NEUTRAL_CHECKS = [...healthChecks, ...staticChecks, ...httpProbeChecks];
const NODE_CHECKS = [...RUNTIME_NEUTRAL_CHECKS, ...nativeModuleChecks, ...nodeRuntimeChecks];
const NODE_SDK_CHECKS = [
  ...healthChecks,
  ...staticChecks,
  ...todoAndDocumentChecks,
  ...nativeModuleChecks,
  ...nodeRuntimeChecks,
  ...httpProbeChecks,
  ...envChecks,
];
const NEXT_ROUTING_AND_CACHE_CHECKS = [
  ...nextRoutingChecks,
  ...nextCacheChecks,
  ...nextOriginCacheChecks,
];
const NEXT_STATE_AND_DATA_CACHE_CHECKS = [
  ...nextStateChecks,
  ...nextDataCacheChecks,
  ...nextOriginDataCacheChecks,
];
const SVELTEKIT_CHECKS = [
  ...healthChecks,
  ...staticChecks,
  ...nativeModuleChecks,
  ...nodeRuntimeChecks,
  ...svelteKitChecks,
];
const BINDING_CHECKS = [...healthChecks, ...staticChecks, ...bindingChecks];
const REALTIME_CHECKS = [...healthChecks, ...realtimeChecks];
const SDK_GO_CHECKS = [
  ...REALTIME_CHECKS,
  ...tasksWireChecks({ task: "exact-echo", topic: "exact-orders", consumer: "exact-audit" }),
];

export const deploy = {
  node: fixture("deploy/node", {
    apps: ["web"],
    checks: NODE_CHECKS,
    on: {
      dev: [defaults],
      aws: [container, apiGateway, cloudflareInFrontOfContainers],
      vps: [defaults, registry, cloudflareOnABox, cloudflareTunnel],
      gcp: [defaults, container, cloudflareOnGoogleCloud, alb],
    },
    sample: { group: "node-http" },
  }),
  go: fixture("deploy/go", {
    apps: ["web"],
    checks: RUNTIME_NEUTRAL_CHECKS,
    on: {
      aws: [container, apiGateway],
      vps: [defaults],
      gcp: [defaults, container],
    },
  }),
  python: fixture("deploy/python", {
    apps: ["web"],
    checks: [...RUNTIME_NEUTRAL_CHECKS, ...vendoredDependencyChecks],
    on: {
      aws: [container, apiGateway],
      vps: [defaults],
      gcp: [defaults, container],
    },
  }),
  rust: fixture("deploy/rust", {
    apps: ["web"],
    checks: RUNTIME_NEUTRAL_CHECKS,
    on: {
      aws: [container, apiGateway],
      vps: [defaults],
      gcp: [defaults, container],
    },
  }),
  next: fixture("deploy/next", {
    apps: ["web"],
    checks: [...NODE_CHECKS, ...NEXT_ROUTING_AND_CACHE_CHECKS],
    on: {
      dev: [defaults],
      aws: [defaults, container, cloudflare, cloudfrontShielded],
      vps: [defaults],
      gcp: [defaults, container, albInFrontOfNext, cloudflareOnGoogleCloud],
    },
    previews: { gcp: [albInFrontOfNext] },
    configOn: { gcp: { allowDegraded: ["edge-runtime", "edge-cache"] } },
  }),
  sveltekit: fixture("deploy/sveltekit", {
    apps: ["web"],
    checks: SVELTEKIT_CHECKS,
    on: {
      dev: [defaults],
      aws: [defaults, container, apiGateway, cloudflare],
      vps: [defaults],
      gcp: [defaults, container],
    },
  }),
  workspace: fixture("deploy/workspace", {
    apps: ["next", "express"],
    checks: NODE_CHECKS,
    on: {
      dev: [defaults],
      aws: [defaults, container, cloudflare, cloudflareInFrontOfMixedComputes],
      vps: [defaults],
      gcp: [defaults, container],
    },
    sample: { group: "node-http", representative: true },
    configOn: { gcp: { allowDegraded: ["edge-cache"] } },
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
      aws: [defaults, container, cloudflare],
      vps: [defaults, cloudflareOnABox, cloudflareTunnel],
      gcp: [defaults, container],
    },
    configOn: { gcp: { allowDegraded: ["edge-runtime", "edge-cache"] } },
  }),
};

export const sdk = {
  node: fixture("sdk/node", {
    apps: ["web"],
    restarts: true,
    redeploys: "where-releases-are-kept",
    checks: [
      ...NODE_SDK_CHECKS,
      ...bindingChecks,
      ...kvChecks,
      ...tasksChecks,
      ...tasksWireChecks({ task: "verbatim", topic: "notices", consumer: "notice-log" }),
      ...realtimeChecks,
    ],
    on: {
      dev: [defaults],
      aws: [defaults, container, apiGateway],
      vps: [defaults],
      gcp: [defaults],
    },
    sample: { group: "node-http" },
  }),
  go: fixture("sdk/go", {
    apps: ["web"],
    devCommands: { web: ["go", "run", "./server"] },
    checks: SDK_GO_CHECKS,
    on: { dev: [defaults], aws: [defaults, apiGateway], vps: [defaults], gcp: [defaults] },
    configOn: { vps: { apps: { web: { path: "./server" } } } },
  }),
  python: fixture("sdk/python", {
    apps: ["web"],
    devCommands: { web: [".venv/bin/python", "main.py"] },
    checks: REALTIME_CHECKS,
    on: { dev: [defaults], aws: [defaults], vps: [defaults], gcp: [defaults] },
  }),
  rust: fixture("sdk/rust", {
    apps: ["web"],
    devCommands: { web: ["cargo", "run", "--quiet"] },
    checks: REALTIME_CHECKS,
    on: { dev: [defaults], aws: [defaults], vps: [defaults], gcp: [defaults] },
  }),
  next: fixture("sdk/next", {
    apps: ["web"],
    checks: [
      ...NODE_SDK_CHECKS,
      ...NEXT_ROUTING_AND_CACHE_CHECKS,
      ...NEXT_STATE_AND_DATA_CACHE_CHECKS,
      ...buildVariablesChecks,
      ...nextClientEnvChecks,
      ...prerenderChecks,
      ...prerenderBucketChecks,
    ],
    on: {
      dev: [defaults],
      aws: [defaults, container, cloudflare],
      vps: [defaults, dockerfile],
      gcp: [defaults],
    },
    configOn: { gcp: { allowDegraded: ["edge-runtime", "edge-cache"] } },
  }),
  sveltekit: fixture("sdk/sveltekit", {
    apps: ["web"],
    checks: [...healthChecks, ...svelteKitClientEnvChecks],
    on: {
      dev: [defaults],
      aws: [defaults],
      vps: [defaults],
      gcp: [defaults],
    },
  }),
  workspace: fixture("sdk/workspace", {
    apps: ["next", "express"],
    checks: NODE_SDK_CHECKS,
    on: {
      dev: [defaults],
      aws: [defaults, container, cloudflare],
      vps: [defaults],
      gcp: [defaults],
    },
    sample: { group: "node-http", representative: true },
    configOn: { gcp: { allowDegraded: ["edge-cache"] } },
  }),
};

export const prerender = {
  nextPpr: fixture("prerender/next-ppr", {
    apps: ["web"],
    checks: [...healthChecks, ...nextPprChecks],
    on: { aws: [defaults, cloudflare] },
  }),
  nextStore: fixture("prerender/next-store", {
    apps: ["web"],
    checks: [...healthChecks, ...nextStoreReadChecks],
    on: { aws: [cloudflare] },
  }),
};

export const preBuild = {
  node: fixture("pre-build/node", {
    apps: ["web"],
    checks: [...healthChecks, ...preBuildChecks],
    on: { vps: [defaults] },
  }),
  next: fixture("pre-build/next", {
    apps: ["web"],
    checks: [...healthChecks, ...preBuildPrerenderChecks],
    on: { vps: [defaults] },
  }),
  failing: fixture("pre-build/failing", {
    apps: ["web"],
    checks: [],
    refusal: failingPreBuildRefusal,
    on: { vps: [defaults] },
  }),
};

export const iac = {
  withSst: fixture("iac/with-sst", {
    apps: ["web"],
    redeploys: true,
    checks: BINDING_CHECKS,
    stack: new SstStack(awsWorld),
    on: { aws: [container, apiGateway] },
  }),
  withPulumi: fixture("iac/with-pulumi", {
    apps: ["web"],
    redeploys: true,
    checks: BINDING_CHECKS,
    stack: new PulumiStack(awsWorld),
    on: { aws: [container, apiGateway] },
  }),
};

export const fixtures: Fixture[] = [
  ...Object.values(deploy),
  ...Object.values(lifecycle),
  ...Object.values(sdk),
  ...Object.values(prerender),
  ...Object.values(preBuild),
  ...Object.values(iac),
];
