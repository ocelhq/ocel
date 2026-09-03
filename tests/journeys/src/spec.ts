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

export type LadderHooks = {
  refuse?: (cell: CellContext) => Promise<void>;
  beforeUp?: (cell: CellContext) => Promise<void>;
  afterDestroy?: (cell: CellContext) => Promise<void>;
};

export type ExampleSpec = {
  name: string;
  dir: string;
  framework?: Framework;
  kind: Kind;
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
    suites: ["health", "static", "product", "probes"],
    apps: ["web"],
  },
  {
    name: "hono",
    dir: "hono",
    framework: "hono",
    kind: "composite",
    suites: ["health", "static", "product", "probes"],
    apps: ["web"],
  },
  {
    name: "fastify",
    dir: "fastify",
    framework: "fastify",
    kind: "composite",
    suites: ["health", "static", "product", "probes"],
    apps: ["web"],
  },
  {
    name: "next",
    dir: "next",
    framework: "next",
    kind: "composite",
    suites: ["health", "static", "product", "probes"],
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
];

export function specForTarget(target: TargetName): ExampleSpec[] {
  return spec.filter((row) => row.targets === undefined || row.targets.includes(target));
}

export function specByName(name: string): ExampleSpec {
  const row = spec.find((candidate) => candidate.name === name);
  if (!row) {
    const known = spec.map((candidate) => candidate.name).join(", ");
    throw new Error(`no example named ${name} in the spec table (${known})`);
  }
  return row;
}
