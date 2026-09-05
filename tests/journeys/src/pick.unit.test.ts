import { describe, expect, it } from "bun:test";
import {
  type CellsFor,
  type Coverage,
  coverageFrom,
  coverCells,
  type Pick,
  pickExamples,
} from "./pick";
import {
  type Cell,
  cellsOf,
  type ExampleSpec,
  preferredOf,
  spec,
  specByName,
  specForTarget,
  variantNameOf,
} from "./spec";

const nodeHttp = spec.filter((row) => row.group === "node-http").map((row) => row.name);

const PREFERRED = preferredOf("node-http") as string;

function names(rows: { name: string }[]): string[] {
  return rows.map((row) => row.name);
}

describe("picking one member of a group", () => {
  it("runs every example when nothing asked for a pick", () => {
    const { chosen, leftOut } = pickExamples(spec, undefined);
    expect(names(chosen)).toEqual(names(spec));
    expect(leftOut).toEqual([]);
  });

  it("runs every ungrouped example whatever the seed", () => {
    const { chosen } = pickExamples(spec, { seed: "7", touched: [] });
    const ungrouped = spec.filter((row) => row.group === undefined);
    expect(names(chosen)).toEqual(expect.arrayContaining(names(ungrouped)));
  });

  it("runs the group's preferred member when the diff touches none of them", () => {
    for (const seed of ["7", "8", "1234"]) {
      const { chosen, leftOut } = pickExamples(spec, { seed, touched: [] });
      expect(names(chosen).filter((name) => nodeHttp.includes(name))).toEqual([PREFERRED]);
      expect(names(leftOut)).toHaveLength(nodeHttp.length - 1);
    }
  });

  it("runs the member the diff touches, and leaves the preferred one out", () => {
    const { chosen, leftOut } = pickExamples(spec, { seed: "7", touched: ["fastify"] });
    expect(names(chosen)).toContain("fastify");
    expect(names(leftOut)).toEqual(["express", "hono", PREFERRED]);
  });

  it("runs every member the diff touches", () => {
    const { chosen, leftOut } = pickExamples(spec, { seed: "7", touched: ["express", "hono"] });
    expect(names(chosen)).toContain("express");
    expect(names(chosen)).toContain("hono");
    expect(names(leftOut)).toEqual(["fastify", PREFERRED]);
  });

  it("keeps the spec's order in what it chose", () => {
    const { chosen } = pickExamples(spec, { seed: "1", touched: ["express", "fastify"] });
    expect(names(chosen)).toEqual(
      names(spec.filter((row) => row.name !== "hono" && row.name !== PREFERRED)),
    );
  });
});

describe("picking one member of a group that names no preferred one", () => {
  const members: ExampleSpec[] = ["one", "two", "three"].map((name) => ({
    name,
    dir: name,
    kind: "composite",
    group: "made-up",
    suites: ["health"],
    apps: ["web"],
  }));

  it("reaches the same member twice for the same seed", () => {
    const once = pickExamples(members, { seed: "1234", touched: [] });
    const twice = pickExamples(members, { seed: "1234", touched: [] });
    expect(names(once.chosen)).toEqual(names(twice.chosen));
  });

  it("reaches every member of the group across seeds", () => {
    const seen = new Set<string>();
    for (let seed = 1; seed <= 50; seed += 1) {
      const { chosen } = pickExamples(members, { seed: String(seed), touched: [] });
      for (const name of names(chosen)) {
        seen.add(name);
      }
    }
    expect([...seen].sort()).toEqual(names(members).sort());
  });
});

const ROWS = specForTarget("aws");
const NODE_HTTP = ROWS.filter((row) => row.group === "node-http");
const SEEDS = ["1", "2", "3", "4", "5", "938"];

const onAws: CellsFor = (example) => cellsOf(example, "aws");

function cellsOn(
  rows: ExampleSpec[],
  cellsFor: CellsFor,
  coverage: Coverage,
  pick: Pick | undefined,
): Cell[] {
  const covered = coverCells(rows, cellsFor, coverage, pick);
  return rows.flatMap((row) => covered.get(row.name) ?? []);
}

const seeded = (seed: string, touched: string[] = []): Pick => ({ seed, touched });

function variantsCovered(rows: ExampleSpec[], cellsFor: CellsFor, pick: Pick): string[] {
  return cellsOn(rows, cellsFor, "covering", pick).map(variantNameOf);
}

describe("covering the variants a group lists, one member each", () => {
  it("runs every cell of every example under full coverage", () => {
    const covered = coverCells(ROWS, onAws, "full", seeded("7"));
    for (const row of ROWS) {
      expect(covered.get(row.name)).toEqual(onAws(row));
    }
  });

  it("runs each variant the group lists on exactly one member", () => {
    for (const seed of SEEDS) {
      const variants = variantsCovered(NODE_HTTP, onAws, seeded(seed));
      for (const variant of ["hello", "container", "api-gateway", "cloudflare"]) {
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
        expect((covered.get(row.name) ?? []).length).toBeGreaterThan(0);
      }
    }
  });

  it("names each cell once", () => {
    for (const seed of SEEDS) {
      const cells = cellsOn(ROWS, onAws, "covering", seeded(seed)).map((cell) => cell.name);
      expect(new Set(cells).size).toBe(cells.length);
    }
  });

  it("runs every cell of an example the diff touches", () => {
    const covered = coverCells(ROWS, onAws, "covering", seeded("7", ["hono"]));
    expect(covered.get("hono")).toEqual(onAws(specByName("hono")));
    expect((covered.get("express") ?? []).length).toBeLessThan(onAws(specByName("express")).length);
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
    const skipping: CellsFor = (example) =>
      onAws(example).filter((cell) => !(example.name === "express" && cell.variant?.name === "hello"));
    for (const seed of SEEDS) {
      const covered = coverCells(NODE_HTTP, skipping, "covering", seeded(seed));
      expect(covered.get("express")?.some((cell) => cell.name === "express-hello")).toBe(false);
      expect(variantsCovered(NODE_HTTP, skipping, seeded(seed))).toContain("hello");
    }
  });

  it("drops a variant no member has a cell for", () => {
    const noCloudflare: CellsFor = (example) =>
      onAws(example).filter((cell) => cell.variant?.name !== "cloudflare");
    expect(variantsCovered(NODE_HTTP, noCloudflare, seeded("3"))).not.toContain("cloudflare");
  });

  it("runs every cell of an example that stands in no group", () => {
    for (const name of ["vps", "dev"] as const) {
      const rows = specForTarget(name);
      const cellsFor: CellsFor = (example) => cellsOf(example, name);
      const covered = coverCells(rows, cellsFor, "covering", seeded("9"));
      for (const row of rows.filter((one) => one.group === undefined)) {
        expect(covered.get(row.name)).toEqual(cellsFor(row));
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
