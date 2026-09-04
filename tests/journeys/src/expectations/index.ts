import { contractRows } from "../contract";
import { contractTitle, type PlannedTest, planTests } from "../plan";
import { type Leg, type Mode, specForTarget, type Suite, type TargetName } from "../spec";
import { gaps } from "./gaps";
import type {
  Affected,
  Edge,
  ExpectationEnvironment,
  Expectations,
  Gap,
  Listed,
  TestPick,
} from "./types";

export type { Edge, ExpectationEnvironment, Expectations, Gap, Listed } from "./types";

export const EDGE_ENV = "OCEL_AWS_EDGE";
export const DEFAULT_EDGE: Edge = "cloudfront";
export const EDGES: readonly Edge[] = ["api-gateway", "cloudfront", "cloudflare"];
export const CONTRACT_LEGS: Leg[] = ["contract", "redeploy", "rollback"];

const EVERY_LEG: Leg[] = ["up", "contract", "redeploy", "rollback", "destroy"];

const EVERY_MODE: Mode[] = ["full", "hello"];

const EVERY_SUITE: Suite[] = [
  "health",
  "static",
  "product",
  "probes",
  "links",
  "next-routing",
  "next-cache",
];

const targetOf: Record<ExpectationEnvironment, TargetName> = {
  aws: "aws",
  "aws.floci": "aws",
  dev: "dev",
  vps: "vps",
  "vps.incus": "vps",
};

function isEdge(named: string): named is Edge {
  return (EDGES as readonly string[]).includes(named);
}

function edgeFor(environment: ExpectationEnvironment): Edge | undefined {
  if (targetOf[environment] !== "aws") {
    return undefined;
  }
  const named = process.env[EDGE_ENV] || DEFAULT_EDGE;
  if (!isEdge(named)) {
    throw new Error(`${EDGE_ENV} is ${named}, and no gap is listed for it (${EDGES.join(", ")})`);
  }
  return named;
}

function titlesOf(pick: TestPick): string[] {
  if (typeof pick === "string") {
    return [pick];
  }
  const legs = pick.legs ?? CONTRACT_LEGS;
  if ("row" in pick) {
    return legs.map((leg) => contractTitle(leg, pick.row));
  }
  const suites = pick.rows === "every" ? EVERY_SUITE : pick.rows;
  const except = new Set(pick.except ?? []);
  return contractRows(suites)
    .filter((row) => !except.has(row.title))
    .flatMap((row) => legs.map((leg) => contractTitle(leg, row.title)));
}

function applies(block: Affected, environment: ExpectationEnvironment, edge: Edge | undefined) {
  if (!block.on.includes(environment)) {
    return false;
  }
  return block.edge === undefined || (edge !== undefined && block.edge.includes(edge));
}

function where(environment: ExpectationEnvironment, edge: Edge | undefined): string {
  return edge ? `${environment} on ${edge}` : environment;
}

function listedOf(gap: Gap): Listed {
  return gap.issue === undefined
    ? { id: gap.id, reason: gap.reason }
    : { id: gap.id, reason: gap.reason, issue: gap.issue };
}

function checkIds(listed: Gap[]) {
  const seen = new Set<string>();
  for (const gap of listed) {
    if (seen.has(gap.id)) {
      throw new Error(`the gap ${gap.id} is listed twice`);
    }
    seen.add(gap.id);
    if (gap.affects.length === 0) {
      throw new Error(`the gap ${gap.id} affects nothing`);
    }
  }
}

function hitsFor(block: Affected, planned: PlannedTest[], said: string): PlannedTest[] {
  const titles = new Set(block.tests.flatMap(titlesOf));
  const hits = planned.filter(
    (test) =>
      (block.cells === undefined || block.cells.includes(test.cell)) && titles.has(test.title),
  );
  for (const cell of block.cells ?? []) {
    if (!hits.some((hit) => hit.cell === cell)) {
      throw new Error(`${said} lists ${cell}, which plans none of the tests named`);
    }
  }
  if (hits.length === 0) {
    throw new Error(`${said} lists nothing that is planned`);
  }
  return hits;
}

export function resolve(
  listed: Gap[],
  environment: ExpectationEnvironment,
  edge: Edge | undefined,
): Expectations {
  checkIds(listed);
  const planned = planTests(specForTarget(targetOf[environment]), EVERY_LEG, EVERY_MODE);
  const out: Expectations = {};
  for (const gap of listed) {
    const carried = new Set<string>();
    for (const block of gap.affects) {
      if (!applies(block, environment, edge)) {
        continue;
      }
      for (const hit of hitsFor(block, planned, `${gap.id} on ${where(environment, edge)}`)) {
        const at = JSON.stringify([hit.cell, hit.title]);
        if (carried.has(at)) {
          continue;
        }
        carried.add(at);
        const cell = (out[hit.cell] ??= {});
        (cell[hit.title] ??= []).push(listedOf(gap));
      }
    }
  }
  return out;
}

export function expectationsFor(environment: ExpectationEnvironment): Expectations {
  return resolve(gaps, environment, edgeFor(environment));
}

export function issueUrl(issue: number): string {
  return `https://github.com/ocelhq/ocel/issues/${issue}`;
}
