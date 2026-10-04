import { createHash } from "node:crypto";
import { type Cell, DEFAULT_VARIANT, type Fixture, sampleGroupOf } from "./matrix/types";

export type Draw = { seed: string; touched: string[] };

export type Coverage = "every-cell" | "sampled";

export const COVERAGES: Coverage[] = ["every-cell", "sampled"];

export type CellsFor = (fixture: Fixture) => Cell[];

function rotation(seed: string, group: string): number {
  return createHash("sha256").update(`${seed}:${group}`).digest().readUInt32BE(0);
}

function sampleGroup(
  group: string,
  members: Fixture[],
  cellsFor: CellsFor,
  draw: Draw,
): Map<string, Cell[]> {
  const chosen = new Map<string, Cell[]>();
  const add = (fixture: Fixture, cell: Cell | undefined) => {
    if (!cell) {
      return;
    }
    const picked = chosen.get(fixture.name) ?? [];
    if (!picked.some((one) => one.name === cell.name)) {
      picked.push(cell);
    }
    chosen.set(fixture.name, picked);
  };
  const cellOf = (fixture: Fixture, variant: string) =>
    cellsFor(fixture).find((cell) => cell.variant.name === variant);

  const free: Fixture[] = [];
  for (const member of members) {
    if (draw.touched.includes(member.name)) {
      chosen.set(member.name, cellsFor(member));
    } else {
      free.push(member);
    }
  }
  if (free.length === 0) {
    return chosen;
  }

  const start = rotation(draw.seed, group);
  const representative =
    free.find((member) => member.sample?.representative) ?? free[start % free.length];
  add(representative, cellOf(representative, DEFAULT_VARIANT));

  const variants: string[] = [];
  for (const member of free) {
    for (const cell of cellsFor(member)) {
      const variant = cell.variant.name;
      if (variant !== DEFAULT_VARIANT && !variants.includes(variant)) {
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
      add(member, cellOf(member, DEFAULT_VARIANT));
    }
  }
  return chosen;
}

export function sample(
  fixtures: Fixture[],
  cellsFor: CellsFor,
  coverage: Coverage,
  draw: Draw | undefined,
): Map<string, Cell[]> {
  if (coverage === "every-cell") {
    return new Map(fixtures.map((one) => [one.name, cellsFor(one)]));
  }
  const asked = draw ?? { seed: "", touched: [] };
  const out = new Map<string, Cell[]>();
  const groups = new Map<string, Fixture[]>();
  for (const one of fixtures) {
    const group = sampleGroupOf(one);
    if (group === undefined) {
      out.set(one.name, cellsFor(one));
      continue;
    }
    groups.set(group, [...(groups.get(group) ?? []), one]);
  }
  for (const [group, members] of groups) {
    for (const [name, cells] of sampleGroup(group, members, cellsFor, asked)) {
      out.set(name, cells);
    }
  }
  return new Map(fixtures.map((one) => [one.name, out.get(one.name) ?? []]));
}
