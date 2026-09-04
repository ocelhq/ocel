import { describe, expect, it } from "vitest";
import { exampleOf, longestFirst } from "./order";
import { spec, specForTarget } from "./spec";

describe("longestFirst", () => {
  it("runs the ladders, then the hellos, then the composites, then the workspace", () => {
    expect(longestFirst(specForTarget("aws")).map((row) => row.kind)).toEqual([
      "ladder",
      "ladder",
      "ladder",
      "hello",
      "hello",
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

describe("exampleOf", () => {
  it("strips the journey module suffix", () => {
    expect(exampleOf("hello-next.journey.test.ts")).toBe("hello-next");
  });
});
