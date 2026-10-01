import type { Compute, RegistryConfig } from "ocel/config";
import type { Check } from "../checks/context";
import type { ExternalStack } from "../stacks";
import type { TestSelector } from "../steps";

export type { Compute, RegistryConfig };

export type TargetName = "dev" | "aws" | "vps" | "gcp";

export const TARGETS: TargetName[] = ["dev", "aws", "vps", "gcp"];

export type Lane = "aws" | "aws.floci" | "dev" | "gcp" | "gcp.floci" | "vps" | "vps.incus";

const TARGET_OF: Record<Lane, TargetName> = {
  aws: "aws",
  "aws.floci": "aws",
  dev: "dev",
  gcp: "gcp",
  "gcp.floci": "gcp",
  vps: "vps",
  "vps.incus": "vps",
};

export const LANES = Object.keys(TARGET_OF) as Lane[];

export function targetOfLane(lane: Lane): TargetName {
  return TARGET_OF[lane];
}

export function laneNamed(name: string): Lane {
  if (!(LANES as string[]).includes(name)) {
    throw new Error(`${JSON.stringify(name)} is no lane a journey runs on (${LANES.join(", ")})`);
  }
  return name as Lane;
}

export type Concern = "deploy" | "lifecycle" | "sdk" | "kv" | "tasks" | "realtime" | "iac";

export const CONCERNS: Concern[] = ["deploy", "lifecycle", "sdk", "kv", "tasks", "realtime", "iac"];

const NAMED_ONLY: Concern[] = ["iac"];

export const UNNAMED_CONCERNS: Concern[] = CONCERNS.filter(
  (concern) => !NAMED_ONLY.includes(concern),
);

export type Edge = "cloudfront" | "api-gateway" | "cloudflare";

export type CacheLayer = "edge" | "origin";

const COMPUTE_WHEN_NONE_IS_NAMED: Record<TargetName, Compute> = {
  dev: "serverless",
  aws: "serverless",
  vps: "container",
  gcp: "serverless",
};

export type Phase = "deploy" | "verify" | "restart" | "redeploy" | "rollback" | "destroy";

export const DEFAULT_VARIANT = "default";

export type ConfigDelta = {
  compute?: Compute;
  computes?: Record<string, Compute>;
  edge?: Edge;
  tunnel?: boolean;
  registry?: RegistryConfig;
};

export type Variant = {
  name: string;
  offeredOn: TargetName[];
  config: ConfigDelta;
  checks?: Check[];
};

export function variant(name: string, shape: Omit<Variant, "name">): Variant {
  if (name === DEFAULT_VARIANT || !/^[a-z][a-z0-9-]*$/.test(name)) {
    throw new Error(`${name} is no variant name: lowercase, dashes, and never ${DEFAULT_VARIANT}`);
  }
  return { name, ...shape };
}

export type Sample = { group: string; representative?: true };

export function sampleGroupOf(fixture: Pick<Fixture, "concern" | "sample">): string | undefined {
  return fixture.sample === undefined ? undefined : `${fixture.concern}/${fixture.sample.group}`;
}

export type Refusal = {
  title: string;
  run: (said: string) => Promise<void>;
};

export type Fixture = {
  name: string;
  concern: Concern;
  apps: string[];
  devCommands?: Record<string, string[]>;
  restarts?: true;
  redeploys?: true | "where-releases-are-kept";
  checks: Check[];
  refusal?: Refusal;
  stack?: ExternalStack;
  on: Partial<Record<TargetName, Variant[]>>;
  sample?: Sample;
};

export function fixture(name: string, shape: Omit<Fixture, "name" | "concern">): Fixture {
  const [concern] = name.split("/");
  if (!(CONCERNS as string[]).includes(concern ?? "") || name.split("/").length !== 2) {
    throw new Error(`${name} is no fixture path: <${CONCERNS.join("|")}>/<name>`);
  }
  return { name, concern: concern as Concern, ...shape };
}

export type Cell = { name: string; fixture: Fixture; variant: Variant; cacheLayer: CacheLayer };

export function cacheLayerOf(target: TargetName, variant: Variant): CacheLayer {
  if (variant.config.edge !== undefined) {
    return "edge";
  }
  const compute = variant.config.compute ?? COMPUTE_WHEN_NONE_IS_NAMED[target];
  return compute === "container" ? "origin" : "edge";
}

export function cellName(fixture: Pick<Fixture, "name">, variant: Variant): string {
  return variant.name === DEFAULT_VARIANT ? fixture.name : `${fixture.name}-${variant.name}`;
}

export type GapScope = {
  on: Lane[];
  fixtures?: Fixture[];
  variants?: Variant[];
  whileUnset?: string[];
  fails: TestSelector[];
  skipsCell?: true;
};

export type Gap = {
  id: string;
  reason: string;
  issue?: number;
  where: GapScope[];
};
