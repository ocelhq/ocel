import { phasesOf, stepsOf } from "./lifecycle";
import {
  type Affected,
  type Cell,
  CONCERNS,
  type Concern,
  cellName,
  type Fixture,
  type Gap,
  type Lane,
  type Phase,
  sampleGroupOf,
  type TargetName,
  targetOfLane,
} from "./matrix/types";
import { type Coverage, type Draw, sample } from "./sample";

export type Ask = {
  concerns: Concern[];
  fixtures: string[];
  variants: string[];
  coverage: Coverage;
  draw?: Draw;
  runSkipped: boolean;
  keep: boolean;
};

export const EVERYTHING: Ask = {
  concerns: CONCERNS,
  fixtures: [],
  variants: [],
  coverage: "full",
  runSkipped: false,
  keep: false,
};

export type Listed = Pick<Gap, "id" | "reason" | "issue">;

export type Expectations = Record<string, Record<string, Listed[]>>;

export type Skipped = Record<string, Listed[]>;

export type PlannedStep = { app: string; title: string; phase?: Phase };

export type PlannedCell = {
  name: string;
  fixture: string;
  variant: string;
  phases: Phase[];
  steps: PlannedStep[];
};

export type Plan = {
  lane: Lane;
  target: TargetName;
  keep: boolean;
  cells: PlannedCell[];
  skipped: Skipped;
  expectations: Expectations;
};

export type PlannedTest = { cell: string; title: string; phase?: Phase };

export function cellKey(cell: string, app: string): string {
  return `${cell}/${app}`;
}

export function testsOf(planned: Pick<Plan, "cells">): PlannedTest[] {
  return planned.cells.flatMap((cell) =>
    cell.steps.map((one) => ({
      cell: cellKey(cell.name, one.app),
      title: one.title,
      ...(one.phase === undefined ? {} : { phase: one.phase }),
    })),
  );
}

export function fixturesOn(fixtures: Fixture[], target: TargetName): Fixture[] {
  return fixtures.filter((one) => one.on[target] !== undefined);
}

export function cellsOn(fixture: Fixture, target: TargetName): Cell[] {
  return (fixture.on[target] ?? []).map((variant) => ({
    name: cellName(fixture, variant),
    fixture,
    variant,
  }));
}

export function cellNamed(fixtures: Fixture[], target: TargetName, name: string): Cell {
  const cells = fixturesOn(fixtures, target).flatMap((one) => cellsOn(one, target));
  const found = cells.find((cell) => cell.name === name);
  if (!found) {
    throw new Error(`${target} runs no cell named ${name}`);
  }
  return found;
}

function longestFirst(cells: Cell[]): Cell[] {
  return [...cells].sort(
    (a, b) => phasesOf(b.fixture, false).length - phasesOf(a.fixture, false).length,
  );
}

type LaneTest = { cell: string; app: string; fixture: string; variant: string; title: string };

function listedOf(gap: Gap): Listed {
  return gap.issue === undefined
    ? { id: gap.id, reason: gap.reason }
    : { id: gap.id, reason: gap.reason, issue: gap.issue };
}

function hitsFor(block: Affected, tests: LaneTest[], said: string): LaneTest[] {
  const titles = new Set(block.tests.flatMap((test) => test.titles));
  const fixtures = block.fixtures?.map((one) => one.name);
  const variants = block.variants?.map((one) => one.name);
  const hits = tests.filter(
    (test) =>
      (fixtures === undefined || fixtures.includes(test.fixture)) &&
      (variants === undefined || variants.includes(test.variant)) &&
      titles.has(test.title),
  );
  const reached = (names: string[] | undefined, of: (hit: LaneTest) => string) => {
    const dead = names?.find((name) => !hits.some((hit) => of(hit) === name));
    if (dead !== undefined) {
      throw new Error(`${said} lists ${dead}, which plans none of the tests named`);
    }
  };
  reached(fixtures, (hit) => hit.fixture);
  reached(variants, (hit) => hit.variant);
  if (hits.length === 0) {
    throw new Error(`${said} lists nothing that is planned`);
  }
  return hits;
}

function resolveGaps(
  gaps: Gap[],
  lane: Lane,
  tests: LaneTest[],
): { expectations: Expectations; skipped: Skipped } {
  const expectations: Expectations = {};
  const skipped: Skipped = {};
  for (const gap of gaps) {
    const carried = new Set<string>();
    const skippedCells = new Set<string>();
    for (const block of gap.affects) {
      if (!block.on.includes(lane)) {
        continue;
      }
      for (const hit of hitsFor(block, tests, `${gap.id} on ${lane}`)) {
        if (block.skip) {
          skippedCells.add(hit.cell);
        }
        const key = cellKey(hit.cell, hit.app);
        const at = JSON.stringify([key, hit.title]);
        if (carried.has(at)) {
          continue;
        }
        carried.add(at);
        const cell = (expectations[key] ??= {});
        (cell[hit.title] ??= []).push(listedOf(gap));
      }
    }
    for (const cell of skippedCells) {
      (skipped[cell] ??= []).push(listedOf(gap));
    }
  }
  return { expectations, skipped };
}

function named(fixtures: Fixture[], names: string[]): Fixture[] {
  if (names.length === 0) {
    return fixtures;
  }
  const unknown = names.filter((name) => !fixtures.some((one) => one.name === name));
  if (unknown.length > 0) {
    const known = fixtures.map((one) => one.name).join(", ");
    throw new Error(`this target runs no fixture named ${unknown.join(", ")} (${known})`);
  }
  return fixtures.filter((one) => names.includes(one.name));
}

function checkMatrix(fixtures: Fixture[]) {
  const seen = new Set<string>();
  const leads = new Map<string, string[]>();
  for (const one of fixtures) {
    if (seen.has(one.name)) {
      throw new Error(`${one.name} is listed twice`);
    }
    seen.add(one.name);
    const placements = Object.entries(one.on);
    if (placements.length === 0) {
      throw new Error(`${one.name} runs on no target`);
    }
    for (const [target, variants = []] of placements) {
      if (variants.length === 0) {
        throw new Error(`${one.name} runs nothing on ${target}`);
      }
      const names = variants.map((variant) => variant.name);
      const twice = names.find((name, index) => names.indexOf(name) !== index);
      if (twice) {
        throw new Error(`${one.name} places the ${twice} variant twice on ${target}`);
      }
      const foreign = variants.find((variant) => !variant.offeredOn.includes(target as TargetName));
      if (foreign) {
        throw new Error(
          `${one.name} asks ${target} for the ${foreign.name} variant, which only ${foreign.offeredOn.join(", ")} offers`,
        );
      }
    }
    const group = sampleGroupOf(one);
    if (group !== undefined && one.sample?.lead) {
      leads.set(group, [...(leads.get(group) ?? []), one.name]);
    }
  }
  for (const [group, led] of leads) {
    if (led.length > 1) {
      throw new Error(`the ${group} group is led by ${led.join(" and ")}`);
    }
  }
}

function checkGaps(gaps: Gap[]) {
  const seen = new Set<string>();
  for (const gap of gaps) {
    if (seen.has(gap.id)) {
      throw new Error(`the gap ${gap.id} is listed twice`);
    }
    seen.add(gap.id);
    if (gap.affects.length === 0) {
      throw new Error(`the gap ${gap.id} affects nothing`);
    }
  }
}

function checkReleaseCycle(fixtures: Fixture[], target: TargetName, releaseCycle: boolean) {
  const redeploying = fixtures.find((one) => one.redeploys);
  if (redeploying && !releaseCycle) {
    throw new Error(
      `${redeploying.name} redeploys, and ${target} has no release cycle to redeploy it with`,
    );
  }
}

function checkVariantsAsked(fixtures: Fixture[], asked: string[]) {
  const known = new Set(
    fixtures.flatMap((one) => Object.values(one.on).flatMap((placed) => placed.map((v) => v.name))),
  );
  const unknown = asked.filter((name) => !known.has(name));
  if (unknown.length > 0) {
    throw new Error(
      `no fixture lists a variant named ${unknown.join(", ")} (${[...known].join(", ")})`,
    );
  }
}

export function plan(input: {
  fixtures: Fixture[];
  gaps: Gap[];
  lane: Lane;
  releaseCycle: boolean;
  ask: Ask;
}): Plan {
  const { fixtures, gaps, lane, releaseCycle, ask } = input;
  checkMatrix(fixtures);
  checkGaps(gaps);
  checkVariantsAsked(fixtures, ask.variants);
  const target = targetOfLane(lane);
  const offered = fixturesOn(fixtures, target);
  checkReleaseCycle(offered, target, releaseCycle);

  const laneTests: LaneTest[] = offered.flatMap((one) =>
    cellsOn(one, target).flatMap((cell) =>
      stepsOf(cell, phasesOf(one, false)).map((step) => ({
        cell: cell.name,
        app: step.app,
        fixture: one.name,
        variant: cell.variant.name,
        title: step.title,
      })),
    ),
  );
  const resolved = resolveGaps(gaps, lane, laneTests);
  const skips = ask.runSkipped ? {} : resolved.skipped;

  const chosen = named(
    offered.filter((one) => ask.concerns.includes(one.concern)),
    ask.fixtures,
  );
  const narrowed = (one: Fixture) =>
    cellsOn(one, target).filter(
      (cell) => ask.variants.length === 0 || ask.variants.includes(cell.variant.name),
    );
  const runnable = (one: Fixture) => narrowed(one).filter((cell) => skips[cell.name] === undefined);
  const covered = sample(chosen, runnable, ask.coverage, ask.draw);

  const skipped: Skipped = {};
  for (const cell of chosen.flatMap(narrowed)) {
    const listed = skips[cell.name];
    if (listed) {
      skipped[cell.name] = listed;
    }
  }

  const cells = longestFirst(chosen.flatMap((one) => covered.get(one.name) ?? [])).map(
    (cell): PlannedCell => {
      const phases = phasesOf(cell.fixture, ask.keep);
      return {
        name: cell.name,
        fixture: cell.fixture.name,
        variant: cell.variant.name,
        phases,
        steps: stepsOf(cell, phases).map(({ app, title, phase }) =>
          phase === undefined ? { app, title } : { app, title, phase },
        ),
      };
    },
  );

  const expectations: Expectations = {};
  for (const test of testsOf({ cells })) {
    const listed = resolved.expectations[test.cell]?.[test.title];
    if (listed) {
      (expectations[test.cell] ??= {})[test.title] = listed;
    }
  }

  return { lane, target, keep: ask.keep, cells, skipped, expectations };
}
