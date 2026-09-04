import { describe, expect, it } from "vitest";
import { type Coverage, coverageFrom, coverVariants, type Pick, pickExamples } from "./pick";
import {
  cellNameOf,
  type ExampleSpec,
  modesOf,
  type Offered,
  preferredOf,
  spec,
  specByName,
  specForTarget,
  type Variant,
  variantsOf,
} from "./spec";
import { targetNamed } from "./targets";

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
    expect(names(chosen)).toEqual(expect.arrayContaining(["express", "hono"]));
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

const AWS = targetNamed("aws");
const ROWS = specForTarget("aws");
const NODE_HTTP = ROWS.filter((row) => row.group === "node-http");
const GROUPS = [NODE_HTTP, [specByName("next")]];
const SEEDS = ["1", "2", "3", "4", "5", "938"];

function cellsOf(rows: ExampleSpec[], offered: Offered, coverage: Coverage, pick: Pick): string[] {
  const covered = coverVariants(rows, offered, coverage, pick);
  return rows.flatMap((row) =>
    (covered.get(row.name) ?? []).map((variant) => cellNameOf(row, variant, offered)),
  );
}

const seeded = (seed: string, touched: string[] = []): Pick => ({ seed, touched });

function coveredVariants(rows: ExampleSpec[], pick: Pick): Variant[] {
  const covered = coverVariants(rows, AWS, "covering", pick);
  return rows.flatMap((row) => covered.get(row.name) ?? []);
}

function holds(rows: ExampleSpec[], pick: Pick, wanted: Partial<Variant>): boolean {
  return coveredVariants(rows, pick).some((variant) =>
    Object.entries(wanted).every(([axis, value]) => variant[axis as keyof Variant] === value),
  );
}

describe("covering the axes a target offers one factor at a time", () => {
  it("runs every variant of every example under full coverage", () => {
    const covered = coverVariants(ROWS, AWS, "full", seeded("7"));
    for (const row of ROWS) {
      expect(covered.get(row.name)).toEqual(variantsOf(row, AWS));
    }
  });

  it("runs each non-default edge at the default mode and compute", () => {
    for (const seed of SEEDS) {
      for (const group of GROUPS) {
        for (const edge of AWS.edges.slice(1)) {
          expect(
            holds(group, seeded(seed), { edge, mode: AWS.modes[0], compute: AWS.computes[0] }),
          ).toBe(true);
        }
      }
    }
  });

  it("runs each non-default compute at the default mode and edge", () => {
    for (const seed of SEEDS) {
      for (const group of GROUPS) {
        for (const compute of AWS.computes.slice(1)) {
          expect(
            holds(group, seeded(seed), { compute, mode: AWS.modes[0], edge: AWS.edges[0] }),
          ).toBe(true);
        }
      }
    }
  });

  it("runs each non-default mode at the default compute and edge", () => {
    for (const seed of SEEDS) {
      for (const group of GROUPS) {
        for (const mode of AWS.modes.slice(1)) {
          expect(
            holds(group, seeded(seed), { mode, compute: AWS.computes[0], edge: AWS.edges[0] }),
          ).toBe(true);
        }
      }
    }
  });

  it("runs the default variant on the group's preferred member", () => {
    const covered = coverVariants(NODE_HTTP, AWS, "covering", seeded("7"));
    expect(covered.get(PREFERRED)).toContainEqual(variantsOf(specByName(PREFERRED), AWS)[0]);
  });

  it("names each cell once", () => {
    for (const seed of SEEDS) {
      const cells = cellsOf(ROWS, AWS, "covering", seeded(seed));
      expect(new Set(cells).size).toBe(cells.length);
    }
  });

  it("runs the whole product of an example the diff touches", () => {
    const covered = coverVariants(ROWS, AWS, "covering", seeded("7", ["hono"]));
    expect(covered.get("hono")).toEqual(variantsOf(specByName("hono"), AWS));
    expect((covered.get("express") ?? []).length).toBeLessThan(
      variantsOf(specByName("express"), AWS).length,
    );
  });

  it("reaches the same cells twice for one seed, and moves them across seeds", () => {
    expect(cellsOf(ROWS, AWS, "covering", seeded("1234"))).toEqual(
      cellsOf(ROWS, AWS, "covering", seeded("1234")),
    );
    const seen = new Set<string>();
    for (let seed = 1; seed <= 40; seed += 1) {
      for (const cell of cellsOf(NODE_HTTP, AWS, "covering", seeded(String(seed)))) {
        seen.add(cell);
      }
    }
    expect(seen.size).toBeGreaterThan(cellsOf(NODE_HTTP, AWS, "covering", seeded("1")).length);
  });

  it("asks a member for no mode it does not run", () => {
    const ladder = specByName("with-transforms");
    const covered = coverVariants([ladder], AWS, "covering", seeded("3"));
    for (const variant of covered.get(ladder.name) ?? []) {
      expect(modesOf(ladder, AWS.modes)).toContain(variant.mode);
    }
  });

  it("leaves a target with one compute and no edge at its full product", () => {
    for (const name of ["vps", "dev"] as const) {
      const target = targetNamed(name);
      const rows = specForTarget(name);
      const covered = coverVariants(rows, target, "covering", seeded("9"));
      for (const row of rows.filter((one) => one.group === undefined)) {
        expect(covered.get(row.name)).toEqual(variantsOf(row, target));
      }
    }
  });
});

describe("the coverage a run asks for", () => {
  it("covers rather than runs the whole product when nothing says", () => {
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
