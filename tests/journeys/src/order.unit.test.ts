import { describe, expect, it } from "bun:test";
import { longestFirst } from "./order";
import { spec, specForTarget } from "./spec";

describe("longestFirst", () => {
  it("runs the ladders, then the composites, then the workspace", () => {
    expect(longestFirst(specForTarget("aws")).map((row) => row.kind)).toEqual([
      "ladder",
      "ladder",
      "ladder",
      "composite",
      "composite",
      "composite",
      "composite",
      "workspace",
    ]);
  });

  it("holds the spec order inside a rank", () => {
    expect(
      longestFirst(spec)
        .filter((row) => row.kind === "composite")
        .map((row) => row.name),
    ).toEqual(["express", "hono", "fastify", "next"]);
  });

  it("leaves the rows it was handed alone", () => {
    const rows = specForTarget("vps");
    longestFirst(rows);
    expect(rows.map((row) => row.name)).toEqual(specForTarget("vps").map((row) => row.name));
  });
});
