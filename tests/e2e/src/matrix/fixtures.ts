import {
  bindingChecks,
  buildVariablesChecks,
  envChecks,
  healthChecks,
  httpProbeChecks,
  kvChecks,
  nativeModuleChecks,
  nextCacheChecks,
  nextDataCacheChecks,
  nextOriginCacheChecks,
  nextOriginDataCacheChecks,
  nextRoutingChecks,
  nextStateChecks,
  nodeRuntimeChecks,
  overlapRefusal,
  prerenderChecks,
  realtimeChecks,
  staticChecks,
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
const BINDING_CHECKS = [...healthChecks, ...staticChecks, ...bindingChecks];
const KV_CHECKS = [...healthChecks, ...staticChecks, ...kvChecks];
const TASKS_NODE_CHECKS = [
  ...healthChecks,
  ...tasksChecks,
  ...tasksWireChecks({ task: "verbatim", topic: "notices", consumer: "notice-log" }),
];
const REALTIME_CHECKS = [...healthChecks, ...realtimeChecks];
const TASKS_GO_CHECKS = [
  ...healthChecks,
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
      aws: [defaults, container, cloudflare],
      vps: [defaults],
      gcp: [defaults, container, alb, cloudflareOnGoogleCloud],
    },
    previews: { gcp: [defaults, alb] },
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
  }),
};

export const sdk = {
  node: fixture("sdk/node", {
    apps: ["web"],
    checks: NODE_SDK_CHECKS,
    on: {
      dev: [defaults],
      aws: [container, apiGateway],
      vps: [defaults],
      gcp: [defaults],
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
      dev: [defaults],
      aws: [defaults, container, cloudflare],
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
  }),
  withTransforms: fixture("sdk/with-transforms", {
    apps: ["web"],
    redeploys: true,
    checks: BINDING_CHECKS,
    on: { aws: [container, apiGateway], vps: [defaults] },
  }),
};

export const buildVariables = {
  next: fixture("build-variables/next", {
    apps: ["web"],
    checks: [...healthChecks, ...buildVariablesChecks],
    on: {
      aws: [defaults],
      gcp: [defaults],
    },
  }),
};

export const prerender = {
  next: fixture("prerender/next", {
    apps: ["web"],
    checks: [...healthChecks, ...prerenderChecks],
    on: { vps: [defaults] },
  }),
  nextDockerfile: fixture("prerender/next-dockerfile", {
    apps: ["web"],
    checks: [...healthChecks, ...prerenderChecks],
    on: { vps: [defaults] },
  }),
};

export const kv = {
  node: fixture("kv/node", {
    apps: ["web"],
    restarts: true,
    redeploys: "where-releases-are-kept",
    checks: KV_CHECKS,
    on: { dev: [defaults], vps: [defaults], aws: [container], gcp: [defaults] },
  }),
  nodeOverlap: fixture("kv/node-overlap", {
    apps: ["web"],
    checks: [],
    refusal: overlapRefusal("kv/node-overlap", ["notes/:id", ":kind/latest"]),
    on: { dev: [defaults], vps: [defaults], aws: [defaults], gcp: [defaults] },
  }),
};

export const tasks = {
  node: fixture("tasks/node", {
    apps: ["web"],
    checks: TASKS_NODE_CHECKS,
    on: { dev: [defaults], vps: [defaults], aws: [apiGateway], gcp: [defaults] },
  }),
  go: fixture("tasks/go", {
    apps: ["web"],
    devCommands: { web: ["go", "run", "./server"] },
    checks: TASKS_GO_CHECKS,
    on: { dev: [defaults], vps: [defaults], aws: [apiGateway], gcp: [defaults] },
  }),
};

export const realtime = {
  node: fixture("realtime/node", {
    apps: ["web"],
    checks: REALTIME_CHECKS,
    on: { dev: [defaults], vps: [defaults], aws: [defaults], gcp: [defaults] },
  }),
  go: fixture("realtime/go", {
    apps: ["web"],
    devCommands: { web: ["go", "run", "./server"] },
    checks: REALTIME_CHECKS,
    on: { dev: [defaults], vps: [defaults], aws: [defaults], gcp: [defaults] },
  }),
  python: fixture("realtime/python", {
    apps: ["web"],
    devCommands: { web: [".venv/bin/python", "main.py"] },
    checks: REALTIME_CHECKS,
    on: { dev: [defaults], vps: [defaults], aws: [defaults], gcp: [defaults] },
  }),
  rust: fixture("realtime/rust", {
    apps: ["web"],
    devCommands: { web: ["cargo", "run", "--quiet"] },
    checks: REALTIME_CHECKS,
    on: { dev: [defaults], vps: [defaults], aws: [defaults], gcp: [defaults] },
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
  ...Object.values(buildVariables),
  ...Object.values(prerender),
  ...Object.values(kv),
  ...Object.values(tasks),
  ...Object.values(realtime),
  ...Object.values(iac),
];
