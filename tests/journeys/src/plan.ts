import {
  type Cell,
  type Concern,
  cellName,
  type Fixture,
  type Gap,
  type GapScope,
  type Lane,
  type Phase,
  sampleGroupOf,
  type TargetName,
  targetOfLane,
  UNNAMED_CONCERNS,
} from "./matrix/types";
import { type Coverage, type Draw, sample } from "./sample";
import { phasesOf, stepsOf } from "./steps";

export type RunFilter = {
  concerns: Concern[];
  fixtures: string[];
  variants: string[];
  coverage: Coverage;
  draw?: Draw;
  runSkipped: boolean;
  keep: boolean;
};

export const NO_FILTER: RunFilter = {
  concerns: UNNAMED_CONCERNS,
  fixtures: [],
  variants: [],
  coverage: "every-cell",
  runSkipped: false,
  keep: false,
};

export type GapRef = Pick<Gap, "id" | "reason" | "issue">;

export type ExpectedFailures = Record<string, Record<string, GapRef[]>>;

export type SkippedCells = Record<string, GapRef[]>;

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
  skipped: SkippedCells;
  expectedFailures: ExpectedFailures;
};

export type PlannedTest = { cell: string; title: string; phase?: Phase };

export function cellApp(cell: string, app: string): string {
  return `${cell}/${app}`;
}

export function testsOf(planned: Pick<Plan, "cells">): PlannedTest[] {
  return planned.cells.flatMap((cell) =>
    cell.steps.map((one) => ({
      cell: cellApp(cell.name, one.app),
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

function gapRefOf(gap: Gap): GapRef {
  return gap.issue === undefined
    ? { id: gap.id, reason: gap.reason }
    : { id: gap.id, reason: gap.reason, issue: gap.issue };
}

function hitsFor(scope: GapScope, tests: LaneTest[], said: string): LaneTest[] {
  const titles = new Set(scope.fails.flatMap((test) => test.titles));
  const fixtures = scope.fixtures?.map((one) => one.name);
  const variants = scope.variants?.map((one) => one.name);
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

function applies(scope: GapScope, lane: Lane, env: NodeJS.ProcessEnv): boolean {
  return (
    scope.on.includes(lane) &&
    (scope.whileUnset === undefined || scope.whileUnset.some((name) => !env[name]?.trim()))
  );
}

function resolveGaps(
  gaps: Gap[],
  lane: Lane,
  env: NodeJS.ProcessEnv,
  tests: LaneTest[],
): { expectedFailures: ExpectedFailures; skipped: SkippedCells } {
  const expectedFailures: ExpectedFailures = {};
  const skipped: SkippedCells = {};
  for (const gap of gaps) {
    const recorded = new Set<string>();
    const skippedCells = new Set<string>();
    for (const scope of gap.where) {
      if (!applies(scope, lane, env)) {
        continue;
      }
      for (const hit of hitsFor(scope, tests, `${gap.id} on ${lane}`)) {
        if (scope.skipsCell) {
          skippedCells.add(hit.cell);
        }
        const key = cellApp(hit.cell, hit.app);
        const at = JSON.stringify([key, hit.title]);
        if (recorded.has(at)) {
          continue;
        }
        recorded.add(at);
        const cell = (expectedFailures[key] ??= {});
        (cell[hit.title] ??= []).push(gapRefOf(gap));
      }
    }
    for (const cell of skippedCells) {
      (skipped[cell] ??= []).push(gapRefOf(gap));
    }
  }
  return { expectedFailures, skipped };
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
  const representatives = new Map<string, string[]>();
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
    if (group !== undefined && one.sample?.representative) {
      representatives.set(group, [...(representatives.get(group) ?? []), one.name]);
    }
  }
  for (const [group, named] of representatives) {
    if (named.length > 1) {
      throw new Error(`the ${group} group is represented by both ${named.join(" and ")}`);
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
    if (gap.where.length === 0) {
      throw new Error(`the gap ${gap.id} applies nowhere`);
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
  filter: RunFilter;
  env: NodeJS.ProcessEnv;
}): Plan {
  const { fixtures, gaps, lane, releaseCycle, filter, env } = input;
  checkMatrix(fixtures);
  checkGaps(gaps);
  checkVariantsAsked(fixtures, filter.variants);
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
  const resolved = resolveGaps(gaps, lane, env, laneTests);
  const skips = filter.runSkipped ? {} : resolved.skipped;

  const chosen = named(
    offered.filter((one) => filter.concerns.includes(one.concern)),
    filter.fixtures,
  );
  const narrowed = (one: Fixture) =>
    cellsOn(one, target).filter(
      (cell) => filter.variants.length === 0 || filter.variants.includes(cell.variant.name),
    );
  const runnable = (one: Fixture) => narrowed(one).filter((cell) => skips[cell.name] === undefined);
  const covered = sample(chosen, runnable, filter.coverage, filter.draw);

  const skipped: SkippedCells = {};
  for (const cell of chosen.flatMap(narrowed)) {
    const listed = skips[cell.name];
    if (listed) {
      skipped[cell.name] = listed;
    }
  }

  const cells = longestFirst(chosen.flatMap((one) => covered.get(one.name) ?? [])).map(
    (cell): PlannedCell => {
      const phases = phasesOf(cell.fixture, filter.keep);
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

  const expectedFailures: ExpectedFailures = {};
  for (const test of testsOf({ cells })) {
    const listed = resolved.expectedFailures[test.cell]?.[test.title];
    if (listed) {
      (expectedFailures[test.cell] ??= {})[test.title] = listed;
    }
  }

  return { lane, target, keep: filter.keep, cells, skipped, expectedFailures };
}
