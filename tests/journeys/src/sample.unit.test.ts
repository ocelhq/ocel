import { describe, expect, it } from "bun:test";
import { type Cell, type Fixture, fixture, variant } from "./matrix/types";
import { defaults } from "./matrix/variants";
import { type CellsFor, type Draw, sample } from "./sample";

const edge = variant("edge", { offeredOn: ["aws"], config: {} });
const box = variant("box", { offeredOn: ["aws"], config: {} });

function member(name: string, representative?: true): Fixture {
  return fixture(name, {
    apps: ["web"],
    checks: [],
    on: { aws: [defaults, edge, box] },
    sample: representative ? { group: "http", representative } : { group: "http" },
  });
}

const representative = member("deploy/representative", true);
const other = member("deploy/other");
const third = member("deploy/third");
const loner = fixture("deploy/loner", {
  apps: ["web"],
  checks: [],
  on: { aws: [defaults, edge] },
});
const GROUP = [representative, other, third];
const ALL = [...GROUP, loner];
const SEEDS = ["1", "2", "3", "4", "5", "938"];

const every: CellsFor = (one) => [
  { name: one.name, fixture: one, variant: defaults },
  { name: `${one.name}-edge`, fixture: one, variant: edge },
  ...(one === loner ? [] : [{ name: `${one.name}-box`, fixture: one, variant: box }]),
];

const seeded = (seed: string, touched: string[] = []): Draw => ({ seed, touched });

function covered(fixtures: Fixture[], cellsFor: CellsFor, draw: Draw): Cell[] {
  const chosen = sample(fixtures, cellsFor, "sampled", draw);
  return fixtures.flatMap((one) => chosen.get(one.name) ?? []);
}

const variantOf = (cell: Cell) => cell.variant.name;

describe("sampling the cells of a group", () => {
  it("runs every cell of every fixture when asked for every cell", () => {
    const chosen = sample(ALL, every, "every-cell", seeded("7"));
    for (const one of ALL) {
      expect(chosen.get(one.name)).toEqual(every(one));
    }
  });

  it("runs each variant the group offers on exactly one member", () => {
    for (const seed of SEEDS) {
      const variants = covered(GROUP, every, seeded(seed)).map(variantOf);
      expect(variants.filter((one) => one === "edge")).toHaveLength(1);
      expect(variants.filter((one) => one === "box")).toHaveLength(1);
    }
  });

  it("runs the default cell on the group's representative", () => {
    for (const seed of SEEDS) {
      const chosen = sample(GROUP, every, "sampled", seeded(seed));
      expect(chosen.get(representative.name)?.map((cell) => cell.name)).toContain(
        representative.name,
      );
    }
  });

  it("runs a default cell on every member that took no variant", () => {
    for (const seed of SEEDS) {
      const chosen = sample(GROUP, every, "sampled", seeded(seed));
      for (const one of GROUP) {
        expect((chosen.get(one.name) ?? []).length).toBeGreaterThan(0);
      }
    }
  });

  it("names each cell once", () => {
    for (const seed of SEEDS) {
      const names = covered(ALL, every, seeded(seed)).map((cell) => cell.name);
      expect(new Set(names).size).toBe(names.length);
    }
  });

  it("runs every cell of a member the diff touches, and spreads the rest", () => {
    for (const seed of SEEDS) {
      const chosen = sample(GROUP, every, "sampled", seeded(seed, ["deploy/other"]));
      expect(chosen.get(other.name)).toEqual(every(other));
      const rest = [representative, third].flatMap((one) => chosen.get(one.name) ?? []);
      expect(rest.length).toBeLessThan([representative, third].flatMap(every).length);
    }
  });

  it("reaches the same cells twice for one seed, and moves them across seeds", () => {
    const names = (seed: string) => covered(GROUP, every, seeded(seed)).map((cell) => cell.name);
    expect(names("1234")).toEqual(names("1234"));
    const seen = new Set<string>();
    for (let seed = 1; seed <= 40; seed += 1) {
      for (const name of names(String(seed))) {
        seen.add(name);
      }
    }
    expect(seen.size).toBeGreaterThan(names("1").length);
  });

  it("hands a variant only to a member that has a cell for it", () => {
    const noOtherBox: CellsFor = (one) =>
      every(one).filter((cell) => !(one === other && cell.variant === box));
    for (const seed of SEEDS) {
      const chosen = sample(GROUP, noOtherBox, "sampled", seeded(seed));
      expect(chosen.get(other.name)?.some((cell) => cell.variant === box)).toBe(false);
      expect(covered(GROUP, noOtherBox, seeded(seed)).map(variantOf)).toContain("box");
    }
  });

  it("drops a variant no member has a cell for", () => {
    const noBox: CellsFor = (one) => every(one).filter((cell) => cell.variant !== box);
    expect(covered(GROUP, noBox, seeded("3")).map(variantOf)).not.toContain("box");
  });

  it("runs every cell of a fixture that samples with no group", () => {
    const chosen = sample(ALL, every, "sampled", seeded("9"));
    expect(chosen.get(loner.name)).toEqual(every(loner));
  });

  it("keeps groups of one name apart across concerns", () => {
    const sdk = member("sdk/representative", true);
    for (const seed of SEEDS) {
      const chosen = sample([representative, other, sdk], every, "sampled", seeded(seed));
      expect(chosen.get(sdk.name)).toEqual(every(sdk));
    }
  });
});
