import type { ContractContext } from "./contract";
import { ladderRows } from "./targets/aws/ladder";
import { pulumiHooks } from "./targets/aws/ladder-pulumi";
import { sstHooks } from "./targets/aws/ladder-sst";
import type { CellContext } from "./targets/types";

export type Framework = "express" | "hono" | "fastify" | "next";

export type Kind = "composite" | "ladder" | "workspace";

export type Mode = "full" | "hello";

export type Group = { name: string; preferred: string };

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
  modes?: Mode[];
  suites: Suite[];
  apps: string[];
  targets?: TargetName[];
  hooks?: LadderHooks;
};

export const groups: Group[] = [{ name: "node-http", preferred: "workspace" }];

const HELLO_SUITES: Suite[] = ["health", "static"];

export const spec: ExampleSpec[] = [
  {
    name: "express",
    dir: "express",
    framework: "express",
    kind: "composite",
    group: "node-http",
    modes: ["full", "hello"],
    suites: ["health", "static", "product", "probes"],
    apps: ["web"],
  },
  {
    name: "hono",
    dir: "hono",
    framework: "hono",
    kind: "composite",
    group: "node-http",
    modes: ["full", "hello"],
    suites: ["health", "static", "product", "probes"],
    apps: ["web"],
  },
  {
    name: "fastify",
    dir: "fastify",
    framework: "fastify",
    kind: "composite",
    group: "node-http",
    modes: ["full", "hello"],
    suites: ["health", "static", "product", "probes"],
    apps: ["web"],
  },
  {
    name: "next",
    dir: "next",
    framework: "next",
    kind: "composite",
    modes: ["full", "hello"],
    suites: ["health", "static", "product", "probes", "next-routing", "next-cache"],
    apps: ["web"],
  },
  {
    name: "workspace",
    dir: "workspace",
    kind: "workspace",
    group: "node-http",
    modes: ["full", "hello"],
    suites: ["health", "static", "product", "probes"],
    apps: ["next", "express"],
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

export function modesOf(example: ExampleSpec, offered: Mode[]): Mode[] {
  return (example.modes ?? ["full"]).filter((mode) => offered.includes(mode));
}

export function cellNameOf(example: ExampleSpec, mode: Mode): string {
  return mode === "hello" ? `${example.name}-hello` : example.name;
}

export function suitesOf(example: ExampleSpec, mode: Mode): Suite[] {
  return mode === "hello"
    ? example.suites.filter((suite) => HELLO_SUITES.includes(suite))
    : example.suites;
}

export function cellNamesOf(example: ExampleSpec): string[] {
  return (example.modes ?? ["full"]).map((mode) => cellNameOf(example, mode));
}

export function preferredOf(group: string): string | undefined {
  const named = groups.find((row) => row.name === group);
  if (!named) {
    return undefined;
  }
  if (!spec.some((row) => row.group === group && row.name === named.preferred)) {
    const members = spec.filter((row) => row.group === group).map((row) => row.name);
    throw new Error(
      `the ${group} group prefers ${named.preferred}, which is no example of that group (${members.join(", ")})`,
    );
  }
  return named.preferred;
}

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
