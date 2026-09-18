import {
  bindingChecks,
  envChecks,
  healthChecks,
  nativeChecks,
  nextCacheChecks,
  nextDataCacheChecks,
  nextRoutingChecks,
  nextStateChecks,
  probeChecks,
  productChecks,
  staticChecks,
  vendoredChecks,
} from "../checks";
import { pulumiLadder } from "../targets/aws/ladder-pulumi";
import { sstLadder } from "../targets/aws/ladder-sst";
import { type Fixture, fixture, LIVES, SERVES } from "./types";
import { apiGateway, cloudflare, container } from "./variants";

const RUNTIME_NEUTRAL = [...healthChecks, ...staticChecks, ...probeChecks];
const SERVED = [...RUNTIME_NEUTRAL, ...nativeChecks];
const STORED = [
  ...healthChecks,
  ...staticChecks,
  ...productChecks,
  ...nativeChecks,
  ...probeChecks,
  ...envChecks,
];
const NEXT_SERVED = [...nextRoutingChecks, ...nextCacheChecks];
const NEXT_STORED = [...nextStateChecks, ...nextDataCacheChecks];
const LADDER = [...healthChecks, ...staticChecks, ...bindingChecks];

export const deploy = {
  node: fixture("deploy/node", {
    runtime: "node",
    apps: ["web"],
    legs: SERVES,
    checks: SERVED,
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
    runtime: "go",
    apps: ["web"],
    legs: SERVES,
    checks: RUNTIME_NEUTRAL,
    on: {
      aws: { variants: [container, apiGateway] },
      vps: { base: true },
      gcp: { base: true, variants: [container] },
    },
  }),
  python: fixture("deploy/python", {
    runtime: "python",
    apps: ["web"],
    legs: SERVES,
    checks: [...RUNTIME_NEUTRAL, ...vendoredChecks],
    on: {
      aws: { variants: [container, apiGateway] },
      vps: { base: true },
      gcp: { base: true, variants: [container] },
    },
  }),
  rust: fixture("deploy/rust", {
    runtime: "rust",
    apps: ["web"],
    legs: SERVES,
    checks: RUNTIME_NEUTRAL,
    on: {
      aws: { variants: [container, apiGateway] },
      vps: { base: true },
      gcp: { base: true, variants: [container] },
    },
  }),
  next: fixture("deploy/next", {
    runtime: "next",
    apps: ["web"],
    legs: SERVES,
    checks: [...SERVED, ...NEXT_SERVED],
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
    legs: SERVES,
    checks: SERVED,
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
    runtime: "next",
    apps: ["web"],
    legs: LIVES,
    checks: [...STORED, ...NEXT_SERVED, ...NEXT_STORED],
    on: {
      aws: { base: true, variants: [container, cloudflare] },
      vps: { base: true },
    },
  }),
};

export const sdk = {
  node: fixture("sdk/node", {
    runtime: "node",
    apps: ["web"],
    legs: SERVES,
    checks: STORED,
    on: {
      dev: { base: true },
      "dev-local": { base: true },
      aws: { variants: [container, apiGateway] },
      vps: { base: true },
    },
    sample: { group: "node-http" },
  }),
  next: fixture("sdk/next", {
    runtime: "next",
    apps: ["web"],
    legs: SERVES,
    checks: [...STORED, ...NEXT_SERVED, ...NEXT_STORED],
    on: {
      dev: { base: true },
      "dev-local": { base: true },
      aws: { base: true, variants: [container, cloudflare] },
      vps: { base: true },
    },
  }),
  workspace: fixture("sdk/workspace", {
    apps: ["next", "express"],
    legs: SERVES,
    checks: STORED,
    on: {
      dev: { base: true },
      "dev-local": { base: true },
      aws: { base: true, variants: [container, cloudflare] },
      vps: { base: true },
    },
    sample: { group: "node-http", lead: true },
  }),
  withTransforms: fixture("sdk/with-transforms", {
    runtime: "node",
    apps: ["web"],
    legs: LIVES,
    checks: LADDER,
    on: { aws: { variants: [container, apiGateway] } },
  }),
  withSst: fixture("sdk/with-sst", {
    runtime: "node",
    apps: ["web"],
    legs: LIVES,
    checks: LADDER,
    ladder: sstLadder,
    on: { aws: { variants: [container, apiGateway] } },
  }),
  withPulumi: fixture("sdk/with-pulumi", {
    runtime: "node",
    apps: ["web"],
    legs: LIVES,
    checks: LADDER,
    ladder: pulumiLadder,
    on: { aws: { variants: [container, apiGateway] } },
  }),
};

export const fixtures: Fixture[] = [
  ...Object.values(deploy),
  ...Object.values(lifecycle),
  ...Object.values(sdk),
];
