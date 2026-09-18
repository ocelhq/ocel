import type { Compute } from "ocel/config";
import type { Check, ContractContext } from "../contract";
import type { TestRef } from "../lifecycle";
import type { CellContext } from "../targets/types";

export type { Compute };

export type TargetName = "dev" | "dev-local" | "aws" | "vps" | "gcp";

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

export type Runtime = "node" | "next" | "go" | "python" | "rust";

export type Edge = "cloudfront" | "api-gateway" | "cloudflare";

export type Leg = "up" | "contract" | "redeploy" | "rollback" | "destroy";

export const SERVES: Leg[] = ["up", "contract", "destroy"];

export const LIVES: Leg[] = ["up", "contract", "redeploy", "rollback", "destroy"];

export const BASE = "base";

export type Base = typeof BASE;

export type ConfigDelta = { compute?: Compute; edge?: Edge };

export type Variant = { name: string; offeredOn: TargetName[]; config: ConfigDelta };

export function variant(name: string, shape: Omit<Variant, "name">): Variant {
  if (name === BASE || !/^[a-z][a-z0-9-]*$/.test(name)) {
    throw new Error(`${name} is no variant name: lowercase, dashes, and never ${BASE}`);
  }
  return { name, ...shape };
}

export type Placement = { base?: true; variants?: Variant[] };

export type LadderPoint = "publish" | "consume" | "outlive" | "prune";

export type LadderCheck = {
  title: string;
  at: LadderPoint;
  run: (cell: CellContext, live?: ContractContext) => Promise<void>;
};

export type Ladder = {
  refuse?: (cell: CellContext) => Promise<void>;
  beforeUp?: (cell: CellContext) => Promise<void>;
  afterDestroy?: (cell: CellContext) => Promise<void>;
  checks?: LadderCheck[];
  sweep?: (runId: string) => Promise<void>;
};

export type Sample = { group: string; lead?: true };

export type Fixture = {
  name: string;
  concern: Concern;
  runtime?: Runtime;
  apps: string[];
  legs: Leg[];
  checks: Check[];
  ladder?: Ladder;
  on: Partial<Record<TargetName, Placement>>;
  sample?: Sample;
};

export function fixture(name: string, shape: Omit<Fixture, "name" | "concern">): Fixture {
  const [concern] = name.split("/");
  if (!(CONCERNS as string[]).includes(concern ?? "") || name.split("/").length !== 2) {
    throw new Error(`${name} is no fixture path: <${CONCERNS.join("|")}>/<name>`);
  }
  return { name, concern: concern as Concern, ...shape };
}

export type Cell = { name: string; fixture: Fixture; variant?: Variant };

export type Affected = {
  on: Lane[];
  fixtures?: Fixture[];
  variants?: Array<Variant | Base>;
  tests: TestRef[];
  skip?: true;
};

export type Gap = {
  id: string;
  reason: string;
  issue?: number;
  affects: Affected[];
};
