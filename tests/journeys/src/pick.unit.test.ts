import { describe, expect, it } from "vitest";
import { pickExamples } from "./pick";
import { type ExampleSpec, preferredOf, spec } from "./spec";

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
