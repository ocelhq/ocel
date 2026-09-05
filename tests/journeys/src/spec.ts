import type { Compute } from "ocel/config";
import type { ContractContext, ContractRow } from "./contract";
import {
  healthRows,
  linkRows,
  nextCacheRows,
  nextDataCacheRows,
  nextRoutingRows,
  nextStateRows,
  probeRows,
  productRows,
  staticRows,
} from "./rows";
import { ladderRows } from "./targets/aws/ladder";
import { pulumiHooks } from "./targets/aws/ladder-pulumi";
import { sstHooks } from "./targets/aws/ladder-sst";
import type { CellContext } from "./targets/types";
import { AWS, BASE, runsOn, type Variant } from "./variants";

export type Framework = "express" | "hono" | "fastify" | "next";

export type Kind = "composite" | "ladder" | "workspace";

export type Edge = "cloudfront" | "api-gateway" | "cloudflare";

export type Concern = "deploy" | "sdk";

export const CONCERNS: Concern[] = ["deploy", "sdk"];

export type { Compute };

export type Group = { concern: Concern; name: string; preferred: string };

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

export type FixtureSpec = {
  name: string;
  concern: Concern;
  dir: string;
  framework?: Framework;
  kind: Kind;
  group?: string;
  rows: ContractRow[];
  apps: string[];
  targets?: TargetName[];
  variants?: Variant[];
  hooks?: LadderHooks;
};

export type Cell = { name: string; fixture: FixtureSpec; variant?: Variant };

export const groups: Group[] = [
  { concern: "deploy", name: "node-http", preferred: "workspace" },
  { concern: "sdk", name: "node-http", preferred: "workspace" },
];

const SERVED = [...healthRows, ...staticRows, ...probeRows];
const STORED = [...healthRows, ...staticRows, ...productRows, ...probeRows];
const NEXT_SERVED = [...nextRoutingRows, ...nextCacheRows];
const NEXT_STORED = [...nextStateRows, ...nextDataCacheRows];
const LADDER = [...healthRows, ...staticRows, ...linkRows];

export const spec: FixtureSpec[] = [
  {
    name: "express",
    concern: "deploy",
    dir: "deploy/express",
    framework: "express",
    kind: "composite",
    group: "node-http",
    rows: SERVED,
    apps: ["web"],
    variants: AWS,
  },
  {
    name: "hono",
    concern: "deploy",
    dir: "deploy/hono",
    framework: "hono",
    kind: "composite",
    group: "node-http",
    rows: SERVED,
    apps: ["web"],
    variants: AWS,
  },
  {
    name: "fastify",
    concern: "deploy",
    dir: "deploy/fastify",
    framework: "fastify",
    kind: "composite",
    group: "node-http",
    rows: SERVED,
    apps: ["web"],
    variants: AWS,
  },
  {
    name: "next",
    concern: "deploy",
    dir: "deploy/next",
    framework: "next",
    kind: "composite",
    rows: [...SERVED, ...NEXT_SERVED],
    apps: ["web"],
    variants: AWS,
  },
  {
    name: "workspace",
    concern: "deploy",
    dir: "deploy/workspace",
    kind: "workspace",
    group: "node-http",
    rows: SERVED,
    apps: ["next", "express"],
    variants: AWS,
  },
  {
    name: "express",
    concern: "sdk",
    dir: "sdk/express",
    framework: "express",
    kind: "composite",
    group: "node-http",
    rows: STORED,
    apps: ["web"],
    variants: AWS,
  },
  {
    name: "hono",
    concern: "sdk",
    dir: "sdk/hono",
    framework: "hono",
    kind: "composite",
    group: "node-http",
    rows: STORED,
    apps: ["web"],
    variants: AWS,
  },
  {
    name: "fastify",
    concern: "sdk",
    dir: "sdk/fastify",
    framework: "fastify",
    kind: "composite",
    group: "node-http",
    rows: STORED,
    apps: ["web"],
    variants: AWS,
  },
  {
    name: "next",
    concern: "sdk",
    dir: "sdk/next",
    framework: "next",
    kind: "composite",
    rows: [...STORED, ...NEXT_SERVED, ...NEXT_STORED],
    apps: ["web"],
    variants: AWS,
  },
  {
    name: "workspace",
    concern: "sdk",
    dir: "sdk/workspace",
    kind: "workspace",
    group: "node-http",
    rows: STORED,
    apps: ["next", "express"],
    variants: AWS,
  },
  {
    name: "with-transforms",
    concern: "sdk",
    dir: "sdk/with-transforms",
    framework: "express",
    kind: "ladder",
    rows: LADDER,
    apps: ["web"],
    targets: ["aws"],
    variants: AWS,
  },
  {
    name: "with-sst",
    concern: "sdk",
    dir: "sdk/with-sst",
    framework: "express",
    kind: "ladder",
    rows: LADDER,
    apps: ["web"],
    targets: ["aws"],
    variants: AWS,
    hooks: { ...sstHooks, rows: ladderRows },
  },
  {
    name: "with-pulumi",
    concern: "sdk",
    dir: "sdk/with-pulumi",
    framework: "express",
    kind: "ladder",
    rows: LADDER,
    apps: ["web"],
    targets: ["aws"],
    variants: AWS,
    hooks: { ...pulumiHooks, rows: ladderRows },
  },
];

export function fixtureNameOf(fixture: FixtureSpec): string {
  return `${fixture.concern}/${fixture.name}`;
}

export function cellNameOf(fixture: FixtureSpec, variant: Variant | undefined): string {
  const named = fixtureNameOf(fixture);
  return variant === undefined ? named : `${named}-${variant.name}`;
}

export function groupKeyOf(fixture: FixtureSpec): string | undefined {
  return fixture.group === undefined ? undefined : `${fixture.concern}/${fixture.group}`;
}

export function variantsOf(fixture: FixtureSpec, target: TargetName): Variant[] {
  const listed = fixture.variants ?? [];
  const names = listed.map((one) => one.name);
  const twice = names.find((name, index) => names.indexOf(name) !== index);
  if (twice) {
    throw new Error(`${fixtureNameOf(fixture)} lists the ${twice} variant twice`);
  }
  return listed.filter((one) => runsOn(one, target));
}

export function cellsOf(fixture: FixtureSpec, target: TargetName): Cell[] {
  return [
    { name: fixtureNameOf(fixture), fixture },
    ...variantsOf(fixture, target).map((variant) => ({
      name: cellNameOf(fixture, variant),
      fixture,
      variant,
    })),
  ];
}

export function variantNameOf(cell: Pick<Cell, "variant">): string {
  return cell.variant?.name ?? BASE;
}

export function preferredOf(key: string): string | undefined {
  const named = groups.find((row) => `${row.concern}/${row.name}` === key);
  if (!named) {
    return undefined;
  }
  const members = spec.filter((row) => groupKeyOf(row) === key);
  if (!members.some((row) => row.name === named.preferred)) {
    throw new Error(
      `the ${key} group prefers ${named.preferred}, which is no fixture of that group (${members
        .map((row) => row.name)
        .join(", ")})`,
    );
  }
  return `${named.concern}/${named.preferred}`;
}

export function specForTarget(target: TargetName): FixtureSpec[] {
  return spec.filter((row) => row.targets === undefined || row.targets.includes(target));
}

export function concernsAsked(asked: string | undefined): Concern[] {
  const named = (asked ?? "").split(/[\s,]+/).filter((name) => name !== "");
  if (named.length === 0) {
    return CONCERNS;
  }
  const unknown = named.filter((name) => !(CONCERNS as string[]).includes(name));
  if (unknown.length > 0) {
    throw new Error(
      `${unknown.join(", ")} is no concern a journey runs (${CONCERNS.join(", ")})`,
    );
  }
  return CONCERNS.filter((concern) => named.includes(concern));
}

export function fixturesNamed(rows: FixtureSpec[], named: string | undefined): FixtureSpec[] {
  const names = (named ?? "")
    .split(",")
    .map((name) => name.trim())
    .filter((name) => name !== "");
  if (names.length === 0) {
    return rows;
  }
  const unknown = names.filter((name) => !rows.some((row) => fixtureNameOf(row) === name));
  if (unknown.length > 0) {
    const known = rows.map(fixtureNameOf).join(", ");
    throw new Error(`this target runs no fixture named ${unknown.join(", ")} (${known})`);
  }
  return rows.filter((row) => names.includes(fixtureNameOf(row)));
}

export function specByName(concern: Concern, name: string): FixtureSpec {
  const row = spec.find((candidate) => candidate.concern === concern && candidate.name === name);
  if (!row) {
    const known = spec
      .filter((candidate) => candidate.concern === concern)
      .map((candidate) => candidate.name)
      .join(", ");
    throw new Error(`no ${concern} fixture named ${name} in the spec table (${known})`);
  }
  return row;
}
