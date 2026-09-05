import { contractTitle, type PlannedTest, planTests } from "../plan";
import { everyRow } from "../rows";
import { cellsOf, type Leg, specForTarget, type TargetName } from "../spec";
import { targetNamed } from "../targets";
import { gaps } from "./gaps";
import type {
  Affected,
  ExpectationEnvironment,
  Expectations,
  Gap,
  Listed,
  Resolved,
  Skipped,
  TestPick,
} from "./types";

export type {
  ExpectationEnvironment,
  Expectations,
  Gap,
  Listed,
  Resolved,
  Skipped,
} from "./types";
export { ENVIRONMENTS } from "./types";

export const CONTRACT_LEGS: Leg[] = ["contract", "redeploy", "rollback"];

const targetOf: Record<ExpectationEnvironment, TargetName> = {
  aws: "aws",
  "aws.floci": "aws",
  dev: "dev",
  vps: "vps",
  "vps.incus": "vps",
};

export function targetOfEnvironment(environment: ExpectationEnvironment): TargetName {
  return targetOf[environment];
}

function titlesOf(pick: TestPick): string[] {
  if (typeof pick === "string") {
    return [pick];
  }
  const legs = pick.legs ?? CONTRACT_LEGS;
  if ("row" in pick) {
    return legs.map((leg) => contractTitle(leg, pick.row));
  }
  const rows = pick.rows === "every" ? everyRow : pick.rows;
  const except = new Set(pick.except ?? []);
  return rows
    .filter((row) => !except.has(row.title))
    .flatMap((row) => legs.map((leg) => contractTitle(leg, row.title)));
}

export function cellOf(key: string): string {
  const parts = key.split("/");
  return parts.slice(0, -1).join("/");
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
      (block.cells === undefined || block.cells.includes(`${test.fixture}/${test.app}`)) &&
      (block.variants === undefined || block.variants.includes(test.variant)) &&
      titles.has(test.title),
  );
  for (const cell of block.cells ?? []) {
    if (!hits.some((hit) => `${hit.fixture}/${hit.app}` === cell)) {
      throw new Error(`${said} lists ${cell}, which plans none of the tests named`);
    }
  }
  for (const variant of block.variants ?? []) {
    if (!hits.some((hit) => hit.variant === variant)) {
      throw new Error(`${said} lists ${variant}, which plans none of the tests named`);
    }
  }
  if (hits.length === 0) {
    throw new Error(`${said} lists nothing that is planned`);
  }
  return hits;
}

export function resolve(listed: Gap[], environment: ExpectationEnvironment): Resolved {
  checkIds(listed);
  const target = targetNamed(targetOf[environment]);
  const cells = specForTarget(target.name).flatMap((fixture) => cellsOf(fixture, target.name));
  const planned = planTests(cells, target.legs);
  const expectations: Expectations = {};
  const skipped: Skipped = {};
  for (const gap of listed) {
    const carried = new Set<string>();
    const skippedCells = new Set<string>();
    for (const block of gap.affects) {
      if (!block.on.includes(environment)) {
        continue;
      }
      for (const hit of hitsFor(block, planned, `${gap.id} on ${environment}`)) {
        if (block.skip) {
          skippedCells.add(cellOf(hit.cell));
        }
        const at = JSON.stringify([hit.cell, hit.title]);
        if (carried.has(at)) {
          continue;
        }
        carried.add(at);
        const cell = (expectations[hit.cell] ??= {});
        (cell[hit.title] ??= []).push(listedOf(gap));
      }
    }
    for (const cell of skippedCells) {
      (skipped[cell] ??= []).push(listedOf(gap));
    }
  }
  return { expectations, skipped };
}

export function expectationsFor(environment: ExpectationEnvironment): Expectations {
  return resolve(gaps, environment).expectations;
}

export function skippedOn(environment: ExpectationEnvironment): Skipped {
  return resolve(gaps, environment).skipped;
}

export function issueUrl(issue: number): string {
  return `https://github.com/ocelhq/ocel/issues/${issue}`;
}
