import { describe, expect, it } from "vitest";
import { type ExampleSpec, examplesNamed } from "./spec";

const rows: ExampleSpec[] = [
  { name: "express", dir: "express", framework: "express", kind: "composite", suites: [], apps: [] },
  { name: "hono", dir: "hono", framework: "hono", kind: "composite", suites: [], apps: [] },
];

describe("examples named in the environment", () => {
  it("is every row when nothing names one", () => {
    expect(examplesNamed(rows, undefined)).toEqual(rows);
  });

  it("is every row when the naming is empty", () => {
    expect(examplesNamed(rows, "  ,  ")).toEqual(rows);
  });

  it("keeps spec order, not the order it was named in", () => {
    expect(examplesNamed(rows, "hono,express").map((row) => row.name)).toEqual([
      "express",
      "hono",
    ]);
  });

  it("tolerates surrounding whitespace", () => {
    expect(examplesNamed(rows, " hono ").map((row) => row.name)).toEqual(["hono"]);
  });

  it("refuses a name this target does not run", () => {
    expect(() => examplesNamed(rows, "express,next")).toThrow(/no example named next/);
  });
});
