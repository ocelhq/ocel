import { createHash } from "node:crypto";
import {
  type Cell,
  type FixtureSpec,
  fixtureNameOf,
  groupKeyOf,
  preferredOf,
  variantNameOf,
} from "./spec";
import { BASE } from "./variants";

export type Pick = { seed: string; touched: string[] };

export type Coverage = "full" | "covering";

export const COVERAGES: Coverage[] = ["full", "covering"];

export type CellsFor = (fixture: FixtureSpec) => Cell[];

function rotation(seed: string, group: string): number {
  return createHash("sha256").update(`${seed}:${group}`).digest().readUInt32BE(0);
}

function memberFor(seed: string, group: string, members: FixtureSpec[]): FixtureSpec {
  return members[rotation(seed, group) % members.length];
}

function untouched(seed: string, group: string, members: FixtureSpec[]): FixtureSpec {
  const preferred = members.find((row) => fixtureNameOf(row) === preferredOf(group));
  return preferred ?? memberFor(seed, group, members);
}

export function pickFixtures(
  rows: FixtureSpec[],
  pick: Pick | undefined,
): { chosen: FixtureSpec[]; leftOut: FixtureSpec[] } {
  if (!pick) {
    return { chosen: rows, leftOut: [] };
  }

  const groups = new Map<string, FixtureSpec[]>();
  for (const row of rows) {
    const key = groupKeyOf(row);
    if (key === undefined) {
      continue;
    }
    const members = groups.get(key) ?? [];
    members.push(row);
    groups.set(key, members);
  }

  const running = new Set<FixtureSpec>();
  for (const [group, members] of groups) {
    const touched = members.filter((row) => pick.touched.includes(row.dir));
    for (const row of touched.length > 0 ? touched : [untouched(pick.seed, group, members)]) {
      running.add(row);
    }
  }

  const chosen = rows.filter((row) => groupKeyOf(row) === undefined || running.has(row));
  const leftOut = rows.filter((row) => groupKeyOf(row) !== undefined && !running.has(row));
  return { chosen, leftOut };
}

export function requestedPick(env: NodeJS.ProcessEnv = process.env): Pick | undefined {
  const seed = (env.OCEL_JOURNEY_SEED ?? "").trim();
  if (seed === "") {
    return undefined;
  }
  return {
    seed,
    touched: (env.OCEL_JOURNEY_TOUCHED ?? "")
      .split(",")
      .map((dir) => dir.trim())
      .filter((dir) => dir !== ""),
  };
}

export function coverageFrom(env: NodeJS.ProcessEnv = process.env): Coverage {
  const asked = (env.OCEL_JOURNEY_COVERAGE ?? "").trim();
  if (asked === "") {
    return "covering";
  }
  if (!(COVERAGES as string[]).includes(asked)) {
    throw new Error(
      `OCEL_JOURNEY_COVERAGE is ${asked}, and a journey covers its cells ${COVERAGES.join(" or ")}`,
    );
  }
  return asked as Coverage;
}

function coverGroup(
  group: string,
  members: FixtureSpec[],
  cellsFor: CellsFor,
  pick: Pick,
): Map<string, Cell[]> {
  const chosen = new Map<string, Cell[]>();
  const add = (fixture: FixtureSpec, cell: Cell | undefined) => {
    if (!cell) {
      return;
    }
    const held = chosen.get(fixtureNameOf(fixture)) ?? [];
    if (!held.some((one) => one.name === cell.name)) {
      held.push(cell);
    }
    chosen.set(fixtureNameOf(fixture), held);
  };
  const cellOf = (fixture: FixtureSpec, variant: string) =>
    cellsFor(fixture).find((cell) => variantNameOf(cell) === variant);

  const free: FixtureSpec[] = [];
  for (const member of members) {
    if (pick.touched.includes(member.dir)) {
      chosen.set(fixtureNameOf(member), cellsFor(member));
    } else {
      free.push(member);
    }
  }
  if (free.length === 0) {
    return chosen;
  }

  const start = rotation(pick.seed, group);
  const lead =
    free.find((row) => fixtureNameOf(row) === preferredOf(group)) ?? free[start % free.length];
  add(lead, cellOf(lead, BASE));

  const variants: string[] = [];
  for (const member of free) {
    for (const cell of cellsFor(member)) {
      const variant = variantNameOf(cell);
      if (variant !== BASE && !variants.includes(variant)) {
        variants.push(variant);
      }
    }
  }
  for (const [index, variant] of variants.entries()) {
    const able = free.filter((member) => cellOf(member, variant) !== undefined);
    const taker = able[(start + index) % able.length];
    add(taker, cellOf(taker, variant));
  }

  for (const member of free) {
    if (!chosen.has(fixtureNameOf(member))) {
      add(member, cellOf(member, BASE));
    }
  }
  return chosen;
}

export function coverCells(
  rows: FixtureSpec[],
  cellsFor: CellsFor,
  coverage: Coverage,
  pick: Pick | undefined,
): Map<string, Cell[]> {
  if (coverage === "full") {
    return new Map(rows.map((row) => [fixtureNameOf(row), cellsFor(row)]));
  }
  const asked = pick ?? { seed: "", touched: [] };
  const out = new Map<string, Cell[]>();
  const groups = new Map<string, FixtureSpec[]>();
  for (const row of rows) {
    const key = groupKeyOf(row);
    if (key === undefined) {
      out.set(fixtureNameOf(row), cellsFor(row));
      continue;
    }
    groups.set(key, [...(groups.get(key) ?? []), row]);
  }
  for (const [group, members] of groups) {
    for (const [name, cells] of coverGroup(group, members, cellsFor, asked)) {
      out.set(name, cells);
    }
  }
  return new Map(rows.map((row) => [fixtureNameOf(row), out.get(fixtureNameOf(row)) ?? []]));
}
