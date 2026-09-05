import { ENVIRONMENTS, type ExpectationEnvironment, type Skipped, skippedOn } from "./expectations";
import { coverageFrom, coverCells, requestedPick } from "./pick";
import {
  type Cell,
  cellsOf,
  type Concern,
  concernsAsked,
  type FixtureSpec,
  fixtureNameOf,
  fixturesNamed,
  spec,
  specForTarget,
  type TargetName,
  variantNameOf,
} from "./spec";
import { BASE } from "./variants";
import type { Target } from "./targets/types";

export const ENVIRONMENT_ENV = "OCEL_JOURNEY_ENVIRONMENT";
export const VARIANTS_ENV = "OCEL_JOURNEY_VARIANTS";
export const SKIPS_ENV = "OCEL_JOURNEY_SKIPS";
export const CONCERN_ENV = "OCEL_JOURNEY_CONCERN";
export const FIXTURES_ENV = "OCEL_JOURNEY_FIXTURES";

export type Selection = {
  fixtures: FixtureSpec[];
  cells: Cell[];
  skipped: Skipped;
};

export function environmentFrom(env: NodeJS.ProcessEnv = process.env): ExpectationEnvironment {
  const named = (env[ENVIRONMENT_ENV] ?? "").trim();
  if (!(ENVIRONMENTS as string[]).includes(named)) {
    throw new Error(
      `${ENVIRONMENT_ENV} is ${JSON.stringify(named)}, and a journey runs on ${ENVIRONMENTS.join(", ")}`,
    );
  }
  return named as ExpectationEnvironment;
}

export function concernsFrom(env: NodeJS.ProcessEnv = process.env): Concern[] {
  return concernsAsked(env[CONCERN_ENV]);
}

export function fixturesFor(target: TargetName, env: NodeJS.ProcessEnv): FixtureSpec[] {
  const concerns = concernsFrom(env);
  const offered = specForTarget(target).filter((row) => concerns.includes(row.concern));
  return fixturesNamed(offered, env[FIXTURES_ENV]);
}

export function variantsAsked(
  rows: FixtureSpec[],
  target: TargetName,
  env: NodeJS.ProcessEnv,
): Set<string> | undefined {
  const named = (env[VARIANTS_ENV] ?? "").split(/[\s,]+/).filter((name) => name !== "");
  if (named.length === 0) {
    return undefined;
  }
  const known = new Set([BASE, ...spec.flatMap((row) => (row.variants ?? []).map((one) => one.name))]);
  const unknown = named.filter((name) => !known.has(name));
  if (unknown.length > 0) {
    throw new Error(
      `no fixture lists a variant named ${unknown.join(", ")} (${[...known].join(", ")})`,
    );
  }
  const offered = new Set(rows.flatMap((row) => cellsOf(row, target).map(variantNameOf)));
  return new Set(named.filter((name) => offered.has(name)));
}

export function skipsLifted(env: NodeJS.ProcessEnv): boolean {
  const asked = (env[SKIPS_ENV] ?? "").trim();
  if (asked === "" || asked === "skip") {
    return false;
  }
  if (asked === "run") {
    return true;
  }
  throw new Error(`${SKIPS_ENV} is ${asked}, and a skipped cell is either skipped or run`);
}

export function selectionFor(
  target: Target,
  environment: ExpectationEnvironment,
  env: NodeJS.ProcessEnv = process.env,
): Selection {
  const fixtures = fixturesFor(target.name, env);
  const asked = variantsAsked(fixtures, target.name, env);
  const skips = skipsLifted(env) ? {} : skippedOn(environment);

  const narrowed = (fixture: FixtureSpec) =>
    cellsOf(fixture, target.name).filter(
      (cell) => asked === undefined || asked.has(variantNameOf(cell)),
    );
  const runnable = (fixture: FixtureSpec) =>
    narrowed(fixture).filter((cell) => skips[cell.name] === undefined);

  const covered = coverCells(fixtures, runnable, coverageFrom(env), requestedPick(env));
  const skipped: Skipped = {};
  for (const fixture of fixtures) {
    for (const cell of narrowed(fixture)) {
      const listed = skips[cell.name];
      if (listed) {
        skipped[cell.name] = listed;
      }
    }
  }
  return {
    fixtures,
    cells: fixtures.flatMap((fixture) => covered.get(fixtureNameOf(fixture)) ?? []),
    skipped,
  };
}

export function cellNamed(selection: Selection, name: string): Cell {
  const cell = selection.cells.find((one) => one.name === name);
  if (!cell) {
    const known = selection.cells.map((one) => one.name).join(", ");
    throw new Error(`this run selects no cell named ${name} (${known})`);
  }
  return cell;
}
