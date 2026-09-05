import { describe, expect, it } from "bun:test";
import {
  type CellsFor,
  type Coverage,
  coverageFrom,
  coverCells,
  type Pick,
  pickFixtures,
} from "./pick";
import {
  type Cell,
  cellsOf,
  type FixtureSpec,
  fixtureNameOf,
  groupKeyOf,
  preferredOf,
  spec,
  specByName,
  specForTarget,
  variantNameOf,
} from "./spec";

const GROUP = "sdk/node-http";

const nodeHttp = spec.filter((row) => groupKeyOf(row) === GROUP).map(fixtureNameOf);

const PREFERRED = preferredOf(GROUP) as string;

function names(rows: FixtureSpec[]): string[] {
  return rows.map(fixtureNameOf);
}

describe("picking one member of a group", () => {
  it("runs every fixture when nothing asked for a pick", () => {
    const { chosen, leftOut } = pickFixtures(spec, undefined);
    expect(names(chosen)).toEqual(names(spec));
    expect(leftOut).toEqual([]);
  });

  it("runs every ungrouped fixture whatever the seed", () => {
    const { chosen } = pickFixtures(spec, { seed: "7", touched: [] });
    const ungrouped = spec.filter((row) => groupKeyOf(row) === undefined);
    expect(names(chosen)).toEqual(expect.arrayContaining(names(ungrouped)));
  });

  it("runs each concern's preferred member when the diff touches none of them", () => {
    for (const seed of ["7", "8", "1234"]) {
      const { chosen, leftOut } = pickFixtures(spec, { seed, touched: [] });
      expect(names(chosen).filter((name) => nodeHttp.includes(name))).toEqual([PREFERRED]);
      expect(names(leftOut).filter((name) => nodeHttp.includes(name))).toHaveLength(
        nodeHttp.length - 1,
      );
    }
  });

  it("picks one member per concern, so a run covers both buckets", () => {
    const { chosen } = pickFixtures(spec, { seed: "7", touched: [] });
    const picked = chosen.filter((row) => groupKeyOf(row) !== undefined);
    expect(new Set(picked.map((row) => row.concern))).toEqual(new Set(["deploy", "sdk"]));
  });

  it("runs the member the diff touches, and leaves the preferred one out", () => {
    const { chosen, leftOut } = pickFixtures(spec, { seed: "7", touched: ["sdk/fastify"] });
    expect(names(chosen)).toContain("sdk/fastify");
    expect(names(leftOut)).toEqual(
      expect.arrayContaining(["sdk/express", "sdk/hono", PREFERRED]),
    );
  });

  it("runs every member the diff touches", () => {
    const { chosen, leftOut } = pickFixtures(spec, {
      seed: "7",
      touched: ["sdk/express", "sdk/hono"],
    });
    expect(names(chosen)).toContain("sdk/express");
    expect(names(chosen)).toContain("sdk/hono");
    expect(names(leftOut)).toContain("sdk/fastify");
    expect(names(leftOut)).toContain(PREFERRED);
  });

  it("keeps the spec's order in what it chose", () => {
    const { chosen } = pickFixtures(spec, {
      seed: "1",
      touched: ["sdk/express", "sdk/fastify"],
    });
    expect(names(chosen)).toEqual(names(spec.filter((row) => chosen.includes(row))));
  });
});

describe("picking one member of a group that names no preferred one", () => {
  const members: FixtureSpec[] = ["one", "two", "three"].map((name) => ({
    name,
    concern: "sdk" as const,
    dir: `sdk/${name}`,
    kind: "composite" as const,
    group: "made-up",
    rows: [],
    apps: ["web"],
  }));

  it("reaches the same member twice for the same seed", () => {
    const once = pickFixtures(members, { seed: "1234", touched: [] });
    const twice = pickFixtures(members, { seed: "1234", touched: [] });
    expect(names(once.chosen)).toEqual(names(twice.chosen));
  });

  it("reaches every member of the group across seeds", () => {
    const seen = new Set<string>();
    for (let seed = 1; seed <= 50; seed += 1) {
      const { chosen } = pickFixtures(members, { seed: String(seed), touched: [] });
      for (const name of names(chosen)) {
        seen.add(name);
      }
    }
    expect([...seen].sort()).toEqual(names(members).sort());
  });
});

const ROWS = specForTarget("aws");
const NODE_HTTP = ROWS.filter((row) => groupKeyOf(row) === GROUP);
const SEEDS = ["1", "2", "3", "4", "5", "938"];

const onAws: CellsFor = (fixture) => cellsOf(fixture, "aws");

function cellsOn(
  rows: FixtureSpec[],
  cellsFor: CellsFor,
  coverage: Coverage,
  pick: Pick | undefined,
): Cell[] {
  const covered = coverCells(rows, cellsFor, coverage, pick);
  return rows.flatMap((row) => covered.get(fixtureNameOf(row)) ?? []);
}

const seeded = (seed: string, touched: string[] = []): Pick => ({ seed, touched });

function variantsCovered(rows: FixtureSpec[], cellsFor: CellsFor, pick: Pick): string[] {
  return cellsOn(rows, cellsFor, "covering", pick).map(variantNameOf);
}

describe("covering the variants a group lists, one member each", () => {
  it("runs every cell of every fixture under full coverage", () => {
    const covered = coverCells(ROWS, onAws, "full", seeded("7"));
    for (const row of ROWS) {
      expect(covered.get(fixtureNameOf(row))).toEqual(onAws(row));
    }
  });

  it("runs each variant the group lists on exactly one member", () => {
    for (const seed of SEEDS) {
      const variants = variantsCovered(NODE_HTTP, onAws, seeded(seed));
      for (const variant of ["container", "api-gateway", "cloudflare"]) {
        expect(variants.filter((one) => one === variant)).toHaveLength(1);
      }
    }
  });

  it("runs the base cell on the group's preferred member", () => {
    const covered = coverCells(NODE_HTTP, onAws, "covering", seeded("7"));
    expect(covered.get(PREFERRED)?.map((cell) => cell.name)).toContain(PREFERRED);
  });

  it("runs a base cell on every member that took no variant", () => {
    for (const seed of SEEDS) {
      const covered = coverCells(NODE_HTTP, onAws, "covering", seeded(seed));
      for (const row of NODE_HTTP) {
        expect((covered.get(fixtureNameOf(row)) ?? []).length).toBeGreaterThan(0);
      }
    }
  });

  it("names each cell once", () => {
    for (const seed of SEEDS) {
      const cells = cellsOn(ROWS, onAws, "covering", seeded(seed)).map((cell) => cell.name);
      expect(new Set(cells).size).toBe(cells.length);
    }
  });

  it("runs every cell of a fixture the diff touches", () => {
    const covered = coverCells(ROWS, onAws, "covering", seeded("7", ["sdk/hono"]));
    expect(covered.get("sdk/hono")).toEqual(onAws(specByName("sdk", "hono")));
    expect((covered.get("sdk/express") ?? []).length).toBeLessThan(
      onAws(specByName("sdk", "express")).length,
    );
  });

  it("reaches the same cells twice for one seed, and moves them across seeds", () => {
    const once = cellsOn(ROWS, onAws, "covering", seeded("1234")).map((cell) => cell.name);
    expect(cellsOn(ROWS, onAws, "covering", seeded("1234")).map((cell) => cell.name)).toEqual(once);
    const seen = new Set<string>();
    for (let seed = 1; seed <= 40; seed += 1) {
      for (const cell of cellsOn(NODE_HTTP, onAws, "covering", seeded(String(seed)))) {
        seen.add(cell.name);
      }
    }
    expect(seen.size).toBeGreaterThan(
      cellsOn(NODE_HTTP, onAws, "covering", seeded("1")).length,
    );
  });

  it("hands a variant only to a member that has a cell for it", () => {
    const skipping: CellsFor = (fixture) =>
      onAws(fixture).filter(
        (cell) => !(fixture.name === "express" && cell.variant?.name === "container"),
      );
    for (const seed of SEEDS) {
      const covered = coverCells(NODE_HTTP, skipping, "covering", seeded(seed));
      expect(
        covered.get("sdk/express")?.some((cell) => cell.name === "sdk/express-container"),
      ).toBe(false);
      expect(variantsCovered(NODE_HTTP, skipping, seeded(seed))).toContain("container");
    }
  });

  it("drops a variant no member has a cell for", () => {
    const noCloudflare: CellsFor = (fixture) =>
      onAws(fixture).filter((cell) => cell.variant?.name !== "cloudflare");
    expect(variantsCovered(NODE_HTTP, noCloudflare, seeded("3"))).not.toContain("cloudflare");
  });

  it("runs every cell of a fixture that stands in no group", () => {
    for (const name of ["vps", "dev"] as const) {
      const rows = specForTarget(name);
      const cellsFor: CellsFor = (fixture) => cellsOf(fixture, name);
      const covered = coverCells(rows, cellsFor, "covering", seeded("9"));
      for (const row of rows.filter((one) => groupKeyOf(one) === undefined)) {
        expect(covered.get(fixtureNameOf(row))).toEqual(cellsFor(row));
      }
    }
  });
});

describe("the coverage a run asks for", () => {
  it("covers rather than runs every cell when nothing says", () => {
    expect(coverageFrom({})).toBe("covering");
    expect(coverageFrom({ OCEL_JOURNEY_COVERAGE: " " })).toBe("covering");
  });

  it("takes the coverage it is handed", () => {
    expect(coverageFrom({ OCEL_JOURNEY_COVERAGE: "full" })).toBe("full");
    expect(coverageFrom({ OCEL_JOURNEY_COVERAGE: "covering" })).toBe("covering");
  });

  it("refuses a coverage nobody runs", () => {
    expect(() => coverageFrom({ OCEL_JOURNEY_COVERAGE: "some" })).toThrow(
      /OCEL_JOURNEY_COVERAGE is some/,
    );
  });
});
