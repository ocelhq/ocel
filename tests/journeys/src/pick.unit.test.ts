import { describe, expect, it } from "bun:test";
import { type CellsFor, type Coverage, coverageFrom, coverCells, type Pick } from "./pick";
import {
  type Cell,
  cellsOf,
  type FixtureSpec,
  fixtureNameOf,
  groupKeyOf,
  preferredOf,
  specByName,
  specForTarget,
  variantNameOf,
} from "./spec";

const GROUP = "sdk/node-http";

const PREFERRED = preferredOf(GROUP) as string;

const ROWS = specForTarget("aws");
const NODE_HTTP = ROWS.filter((row) => groupKeyOf(row) === GROUP);
const DEPLOY_NODE_HTTP = ROWS.filter((row) => groupKeyOf(row) === "deploy/node-http");
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

  it("runs every cell of a fixture the diff touches, and spreads the group it leaves alone", () => {
    for (const seed of SEEDS) {
      const covered = coverCells(ROWS, onAws, "covering", seeded(seed, ["sdk/node"]));
      expect(covered.get("sdk/node")).toEqual(onAws(specByName("sdk", "node")));
      const deploy = DEPLOY_NODE_HTTP.flatMap((row) => covered.get(fixtureNameOf(row)) ?? []);
      expect(deploy.length).toBeLessThan(DEPLOY_NODE_HTTP.flatMap(onAws).length);
    }
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
    expect(seen.size).toBeGreaterThan(cellsOn(NODE_HTTP, onAws, "covering", seeded("1")).length);
  });

  it("hands a variant only to a member that has a cell for it", () => {
    const skipping: CellsFor = (fixture) =>
      onAws(fixture).filter(
        (cell) => !(fixture.name === "node" && cell.variant?.name === "container"),
      );
    for (const seed of SEEDS) {
      const covered = coverCells(NODE_HTTP, skipping, "covering", seeded(seed));
      expect(covered.get("sdk/node")?.some((cell) => cell.name === "sdk/node-container")).toBe(
        false,
      );
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
