import { createHash } from "node:crypto";
import { type Cell, type ExampleSpec, preferredOf, variantNameOf } from "./spec";
import { BASE } from "./variants";

export type Pick = { seed: string; touched: string[] };

export type Coverage = "full" | "covering";

export const COVERAGES: Coverage[] = ["full", "covering"];

export type CellsFor = (example: ExampleSpec) => Cell[];

function rotation(seed: string, group: string): number {
  return createHash("sha256").update(`${seed}:${group}`).digest().readUInt32BE(0);
}

function memberFor(seed: string, group: string, members: ExampleSpec[]): ExampleSpec {
  return members[rotation(seed, group) % members.length];
}

function untouched(seed: string, group: string, members: ExampleSpec[]): ExampleSpec {
  const preferred = members.find((row) => row.name === preferredOf(group));
  return preferred ?? memberFor(seed, group, members);
}

export function pickExamples(
  rows: ExampleSpec[],
  pick: Pick | undefined,
): { chosen: ExampleSpec[]; leftOut: ExampleSpec[] } {
  if (!pick) {
    return { chosen: rows, leftOut: [] };
  }

  const groups = new Map<string, ExampleSpec[]>();
  for (const row of rows) {
    if (row.group === undefined) {
      continue;
    }
    const members = groups.get(row.group) ?? [];
    members.push(row);
    groups.set(row.group, members);
  }

  const running = new Set<ExampleSpec>();
  for (const [group, members] of groups) {
    const touched = members.filter((row) => pick.touched.includes(row.dir));
    for (const row of touched.length > 0 ? touched : [untouched(pick.seed, group, members)]) {
      running.add(row);
    }
  }

  const chosen = rows.filter((row) => row.group === undefined || running.has(row));
  const leftOut = rows.filter((row) => row.group !== undefined && !running.has(row));
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
  members: ExampleSpec[],
  cellsFor: CellsFor,
  pick: Pick,
): Map<string, Cell[]> {
  const chosen = new Map<string, Cell[]>();
  const add = (example: ExampleSpec, cell: Cell | undefined) => {
    if (!cell) {
      return;
    }
    const held = chosen.get(example.name) ?? [];
    if (!held.some((one) => one.name === cell.name)) {
      held.push(cell);
    }
    chosen.set(example.name, held);
  };
  const cellOf = (example: ExampleSpec, variant: string) =>
    cellsFor(example).find((cell) => variantNameOf(cell) === variant);

  const free: ExampleSpec[] = [];
  for (const member of members) {
    if (pick.touched.includes(member.dir)) {
      chosen.set(member.name, cellsFor(member));
    } else {
      free.push(member);
    }
  }
  if (free.length === 0) {
    return chosen;
  }

  const start = rotation(pick.seed, group);
  const lead = free.find((row) => row.name === preferredOf(group)) ?? free[start % free.length];
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
    if (!chosen.has(member.name)) {
      add(member, cellOf(member, BASE));
    }
  }
  return chosen;
}

export function coverCells(
  rows: ExampleSpec[],
  cellsFor: CellsFor,
  coverage: Coverage,
  pick: Pick | undefined,
): Map<string, Cell[]> {
  if (coverage === "full") {
    return new Map(rows.map((row) => [row.name, cellsFor(row)]));
  }
  const asked = pick ?? { seed: "", touched: [] };
  const out = new Map<string, Cell[]>();
  const groups = new Map<string, ExampleSpec[]>();
  for (const row of rows) {
    if (row.group === undefined) {
      out.set(row.name, cellsFor(row));
      continue;
    }
    groups.set(row.group, [...(groups.get(row.group) ?? []), row]);
  }
  for (const [group, members] of groups) {
    for (const [name, cells] of coverGroup(group, members, cellsFor, asked)) {
      out.set(name, cells);
    }
  }
  return new Map(rows.map((row) => [row.name, out.get(row.name) ?? []]));
}
