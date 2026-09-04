import { contractRows } from "../contract";
import { contractTitle, type PlannedTest, planTests } from "../plan";
import { type Leg, specForTarget, type Suite, type TargetName } from "../spec";
import { targetNamed } from "../targets";
import { gaps } from "./gaps";
import type {
  Affected,
  ExpectationEnvironment,
  Expectations,
  Gap,
  Listed,
  TestPick,
} from "./types";

export type { Compute, Edge, ExpectationEnvironment, Expectations, Gap, Listed } from "./types";

export const CONTRACT_LEGS: Leg[] = ["contract", "redeploy", "rollback"];

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
      (block.cells === undefined || block.cells.includes(test.cell)) &&
      (block.compute === undefined || block.compute.includes(test.variant.compute)) &&
      (block.edge === undefined ||
        (test.variant.edge !== undefined && block.edge.includes(test.variant.edge))) &&
      titles.has(test.title),
  );
  for (const cell of block.cells ?? []) {
    if (!hits.some((hit) => hit.cell === cell)) {
      throw new Error(`${said} lists ${cell}, which plans none of the tests named`);
    }
  }
  for (const compute of block.compute ?? []) {
    if (!hits.some((hit) => hit.variant.compute === compute)) {
      throw new Error(`${said} lists ${compute}, which plans none of the tests named`);
    }
  }
  for (const edge of block.edge ?? []) {
    if (!hits.some((hit) => hit.variant.edge === edge)) {
      throw new Error(`${said} lists ${edge}, which plans none of the tests named`);
    }
  }
  if (hits.length === 0) {
    throw new Error(`${said} lists nothing that is planned`);
  }
  return hits;
}

export function resolve(listed: Gap[], environment: ExpectationEnvironment): Expectations {
  checkIds(listed);
  const target = targetNamed(targetOf[environment]);
  const planned = planTests(specForTarget(target.name), target.legs, target);
  const out: Expectations = {};
  for (const gap of listed) {
    const carried = new Set<string>();
    for (const block of gap.affects) {
      if (!block.on.includes(environment)) {
        continue;
      }
      for (const hit of hitsFor(block, planned, `${gap.id} on ${environment}`)) {
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
  return resolve(gaps, environment);
}

export function issueUrl(issue: number): string {
  return `https://github.com/ocelhq/ocel/issues/${issue}`;
}
