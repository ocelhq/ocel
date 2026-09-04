import { describe, expect, it } from "vitest";
import { pickExamples } from "./pick";
import { spec } from "./spec";

const nodeHttp = spec.filter((row) => row.group === "node-http").map((row) => row.name);

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

  it("runs one member of the group when the diff touches none of them", () => {
    const { chosen, leftOut } = pickExamples(spec, { seed: "7", touched: [] });
    expect(names(chosen).filter((name) => nodeHttp.includes(name))).toHaveLength(1);
    expect(names(leftOut)).toHaveLength(nodeHttp.length - 1);
  });

  it("runs the member the diff touches", () => {
    const { chosen, leftOut } = pickExamples(spec, { seed: "7", touched: ["fastify"] });
    expect(names(chosen)).toContain("fastify");
    expect(names(leftOut)).toEqual(["express", "hono"]);
  });

  it("runs every member the diff touches", () => {
    const { chosen, leftOut } = pickExamples(spec, { seed: "7", touched: ["express", "hono"] });
    expect(names(chosen)).toEqual(expect.arrayContaining(["express", "hono"]));
    expect(names(leftOut)).toEqual(["fastify"]);
  });

  it("reaches the same member twice for the same seed", () => {
    const once = pickExamples(spec, { seed: "1234", touched: [] });
    const twice = pickExamples(spec, { seed: "1234", touched: [] });
    expect(names(once.chosen)).toEqual(names(twice.chosen));
  });

  it("reaches every member of the group across seeds", () => {
    const seen = new Set<string>();
    for (let seed = 1; seed <= 50; seed += 1) {
      const { chosen } = pickExamples(spec, { seed: String(seed), touched: [] });
      for (const name of names(chosen)) {
        if (nodeHttp.includes(name)) {
          seen.add(name);
        }
      }
    }
    expect([...seen].sort()).toEqual([...nodeHttp].sort());
  });

  it("keeps the spec's order in what it chose", () => {
    const { chosen } = pickExamples(spec, { seed: "1", touched: ["express", "fastify"] });
    expect(names(chosen)).toEqual(names(spec.filter((row) => row.name !== "hono")));
  });
});
