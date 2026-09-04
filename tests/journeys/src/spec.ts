import type { ContractContext } from "./contract";
import { ladderRows } from "./targets/aws/ladder";
import { pulumiHooks } from "./targets/aws/ladder-pulumi";
import { sstHooks } from "./targets/aws/ladder-sst";
import type { CellContext } from "./targets/types";

export type Framework = "express" | "hono" | "fastify" | "next";

export type Kind = "composite" | "hello" | "ladder" | "workspace";

export type Suite =
  | "health"
  | "static"
  | "product"
  | "probes"
  | "links"
  | "next-routing"
  | "next-cache";

export type TargetName = "dev" | "aws" | "vps";

export type Leg = "up" | "contract" | "redeploy" | "rollback" | "destroy";

export type LadderPhase = "publish" | "consume" | "outlive" | "prune";

export type LadderRow = {
  title: string;
  phase: LadderPhase;
  run: (cell: CellContext, live?: ContractContext) => Promise<void>;
};

export function ladderTitle(phase: LadderPhase, title: string): string {
  return `${phase} · ${title}`;
}

export type LadderHooks = {
  refuse?: (cell: CellContext) => Promise<void>;
  beforeUp?: (cell: CellContext) => Promise<void>;
  afterDestroy?: (cell: CellContext) => Promise<void>;
  rows?: LadderRow[];
};

export type ExampleSpec = {
  name: string;
  dir: string;
  framework?: Framework;
  kind: Kind;
  group?: string;
  suites: Suite[];
  apps: string[];
  targets?: TargetName[];
  hooks?: LadderHooks;
};

export const spec: ExampleSpec[] = [
  {
    name: "express",
    dir: "express",
    framework: "express",
    kind: "composite",
    group: "node-http",
    suites: ["health", "static", "product", "probes"],
    apps: ["web"],
  },
  {
    name: "hono",
    dir: "hono",
    framework: "hono",
    kind: "composite",
    group: "node-http",
    suites: ["health", "static", "product", "probes"],
    apps: ["web"],
  },
  {
    name: "fastify",
    dir: "fastify",
    framework: "fastify",
    kind: "composite",
    group: "node-http",
    suites: ["health", "static", "product", "probes"],
    apps: ["web"],
  },
  {
    name: "next",
    dir: "next",
    framework: "next",
    kind: "composite",
    suites: ["health", "static", "product", "probes", "next-routing", "next-cache"],
    apps: ["web"],
  },
  {
    name: "hello-express",
    dir: "hello-express",
    framework: "express",
    kind: "hello",
    suites: ["health", "static"],
    apps: ["web"],
  },
  {
    name: "hello-next",
    dir: "hello-next",
    framework: "next",
    kind: "hello",
    suites: ["health", "static"],
    apps: ["web"],
  },
  {
    name: "workspace",
    dir: "workspace",
    kind: "workspace",
    suites: ["health", "static", "product", "probes"],
    apps: ["next", "express", "hono"],
  },
  {
    name: "with-transforms",
    dir: "with-transforms",
    framework: "express",
    kind: "ladder",
    suites: ["health", "static", "links"],
    apps: ["web"],
    targets: ["aws"],
  },
  {
    name: "with-sst",
    dir: "with-sst",
    framework: "express",
    kind: "ladder",
    suites: ["health", "static", "links"],
    apps: ["web"],
    targets: ["aws"],
    hooks: { ...sstHooks, rows: ladderRows },
  },
  {
    name: "with-pulumi",
    dir: "with-pulumi",
    framework: "express",
    kind: "ladder",
    suites: ["health", "static", "links"],
    apps: ["web"],
    targets: ["aws"],
    hooks: { ...pulumiHooks, rows: ladderRows },
  },
];

export function specForTarget(target: TargetName): ExampleSpec[] {
  return spec.filter((row) => row.targets === undefined || row.targets.includes(target));
}

export function examplesNamed(rows: ExampleSpec[], named: string | undefined): ExampleSpec[] {
  const names = (named ?? "")
    .split(",")
    .map((name) => name.trim())
    .filter((name) => name !== "");
  if (names.length === 0) {
    return rows;
  }
  const unknown = names.filter((name) => !rows.some((row) => row.name === name));
  if (unknown.length > 0) {
    const known = rows.map((row) => row.name).join(", ");
    throw new Error(`this target runs no example named ${unknown.join(", ")} (${known})`);
  }
  return rows.filter((row) => names.includes(row.name));
}

export function specByName(name: string): ExampleSpec {
  const row = spec.find((candidate) => candidate.name === name);
  if (!row) {
    const known = spec.map((candidate) => candidate.name).join(", ");
    throw new Error(`no example named ${name} in the spec table (${known})`);
  }
  return row;
}
