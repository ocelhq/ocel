import type { Compute } from "ocel/config";
import type { Check } from "../checks/context";
import type { TestRef } from "../lifecycle";
import type { ExternalStack } from "../targets/aws/stacks/bindings";

export type { Compute };

export type TargetName = "dev" | "dev-local" | "aws" | "vps" | "gcp";

export const TARGETS: TargetName[] = ["dev", "dev-local", "aws", "vps", "gcp"];

export type Lane =
  | "aws"
  | "aws.floci"
  | "dev"
  | "dev-local"
  | "gcp"
  | "gcp.floci"
  | "vps"
  | "vps.incus";

const TARGET_OF: Record<Lane, TargetName> = {
  aws: "aws",
  "aws.floci": "aws",
  dev: "dev",
  "dev-local": "dev-local",
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

export type Concern = "deploy" | "lifecycle" | "sdk";

export const CONCERNS: Concern[] = ["deploy", "lifecycle", "sdk"];

export type Edge = "cloudfront" | "api-gateway" | "cloudflare";

export type Phase = "deploy" | "verify" | "redeploy" | "rollback" | "destroy";

export const DEFAULT_VARIANT = "default";

export type ConfigDelta = { compute?: Compute; edge?: Edge };

export type Variant = { name: string; offeredOn: TargetName[]; config: ConfigDelta };

export function variant(name: string, shape: Omit<Variant, "name">): Variant {
  if (name === DEFAULT_VARIANT || !/^[a-z][a-z0-9-]*$/.test(name)) {
    throw new Error(`${name} is no variant name: lowercase, dashes, and never ${DEFAULT_VARIANT}`);
  }
  return { name, ...shape };
}

export type Sample = { group: string; lead?: true };

export function sampleGroupOf(fixture: Pick<Fixture, "concern" | "sample">): string | undefined {
  return fixture.sample === undefined ? undefined : `${fixture.concern}/${fixture.sample.group}`;
}

export type Fixture = {
  name: string;
  concern: Concern;
  apps: string[];
  redeploys?: true;
  checks: Check[];
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

export type Cell = { name: string; fixture: Fixture; variant: Variant };

export function cellName(fixture: Pick<Fixture, "name">, variant: Variant): string {
  return variant.name === DEFAULT_VARIANT ? fixture.name : `${fixture.name}-${variant.name}`;
}

export type Affected = {
  on: Lane[];
  fixtures?: Fixture[];
  variants?: Variant[];
  tests: TestRef[];
  skip?: true;
};

export type Gap = {
  id: string;
  reason: string;
  issue?: number;
  affects: Affected[];
};
