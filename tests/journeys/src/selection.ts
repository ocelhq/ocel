import { ENVIRONMENTS, type ExpectationEnvironment, type Skipped, skippedOn } from "./expectations";
import { coverageFrom, coverCells, requestedPick } from "./pick";
import {
  type Cell,
  cellsOf,
  type ExampleSpec,
  examplesNamed,
  specForTarget,
  type TargetName,
  variantNameOf,
} from "./spec";
import type { Target } from "./targets/types";

export const ENVIRONMENT_ENV = "OCEL_JOURNEY_ENVIRONMENT";
export const VARIANTS_ENV = "OCEL_JOURNEY_VARIANTS";
export const SKIPS_ENV = "OCEL_JOURNEY_SKIPS";

export type Selection = {
  examples: ExampleSpec[];
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

export function variantsAsked(
  rows: ExampleSpec[],
  target: TargetName,
  env: NodeJS.ProcessEnv,
): Set<string> | undefined {
  const named = (env[VARIANTS_ENV] ?? "").split(/[\s,]+/).filter((name) => name !== "");
  if (named.length === 0) {
    return undefined;
  }
  const offered = new Set(rows.flatMap((row) => cellsOf(row, target).map(variantNameOf)));
  const unknown = named.filter((name) => !offered.has(name));
  if (unknown.length > 0) {
    throw new Error(
      `${target} runs no variant named ${unknown.join(", ")} (${[...offered].join(", ")})`,
    );
  }
  return new Set(named);
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
  const examples = examplesNamed(specForTarget(target.name), env.OCEL_EXAMPLES);
  const asked = variantsAsked(examples, target.name, env);
  const skips = skipsLifted(env) ? {} : skippedOn(environment);

  const narrowed = (example: ExampleSpec) =>
    cellsOf(example, target.name).filter(
      (cell) => asked === undefined || asked.has(variantNameOf(cell)),
    );
  const runnable = (example: ExampleSpec) =>
    narrowed(example).filter((cell) => skips[cell.name] === undefined);

  const covered = coverCells(examples, runnable, coverageFrom(env), requestedPick(env));
  const skipped: Skipped = {};
  for (const example of examples) {
    for (const cell of narrowed(example)) {
      const listed = skips[cell.name];
      if (listed) {
        skipped[cell.name] = listed;
      }
    }
  }
  return {
    examples,
    cells: examples.flatMap((example) => covered.get(example.name) ?? []),
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
