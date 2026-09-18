import { CONCERNS, type Concern } from "../matrix/types";
import type { RunFilter } from "../plan";
import { COVERAGES, type Coverage, type Draw } from "../sample";

function listed(value: string | undefined, separators: RegExp): string[] {
  return (value ?? "")
    .split(separators)
    .map((name) => name.trim())
    .filter((name) => name !== "");
}

export function concernsNamed(asked: string | undefined): Concern[] {
  const named = listed(asked, /[\s,]+/);
  if (named.length === 0) {
    return CONCERNS;
  }
  const unknown = named.filter((name) => !(CONCERNS as string[]).includes(name));
  if (unknown.length > 0) {
    throw new Error(`${unknown.join(", ")} is no concern a journey runs (${CONCERNS.join(", ")})`);
  }
  return CONCERNS.filter((concern) => named.includes(concern));
}

function coverageFrom(env: NodeJS.ProcessEnv): Coverage {
  const asked = (env.OCEL_JOURNEY_COVERAGE ?? "").trim();
  if (asked === "") {
    return "sampled";
  }
  if (!(COVERAGES as string[]).includes(asked)) {
    throw new Error(
      `OCEL_JOURNEY_COVERAGE is ${asked}, and a journey runs ${COVERAGES.join(" or ")}`,
    );
  }
  return asked as Coverage;
}

function drawFrom(env: NodeJS.ProcessEnv): Draw | undefined {
  const seed = (env.OCEL_JOURNEY_SEED ?? "").trim();
  if (seed === "") {
    return undefined;
  }
  return { seed, touched: listed(env.OCEL_JOURNEY_TOUCHED, /,/) };
}

function skipsLifted(env: NodeJS.ProcessEnv): boolean {
  const asked = (env.OCEL_JOURNEY_SKIPS ?? "").trim();
  if (asked === "" || asked === "skip") {
    return false;
  }
  if (asked === "run") {
    return true;
  }
  throw new Error(`OCEL_JOURNEY_SKIPS is ${asked}, and a skipped cell is either skipped or run`);
}

function keepsStanding(env: NodeJS.ProcessEnv): boolean {
  const asked = (env.OCEL_JOURNEY_KEEP ?? "").trim().toLowerCase();
  if (asked === "") {
    return false;
  }
  if (asked === "1" || asked === "true" || asked === "yes") {
    return true;
  }
  throw new Error(`OCEL_JOURNEY_KEEP is ${asked}, and a cell is either kept standing or destroyed`);
}

export function filterFrom(env: NodeJS.ProcessEnv): RunFilter {
  const draw = drawFrom(env);
  return {
    concerns: concernsNamed(env.OCEL_JOURNEY_CONCERN),
    fixtures: listed(env.OCEL_JOURNEY_FIXTURES, /,/),
    variants: listed(env.OCEL_JOURNEY_VARIANTS, /[\s,]+/),
    coverage: coverageFrom(env),
    ...(draw === undefined ? {} : { draw }),
    runSkipped: skipsLifted(env),
    keep: keepsStanding(env),
  };
}
