import { describe, expect, it } from "vitest";
import {
  cellNameOf,
  cellNamesOf,
  type ExampleSpec,
  examplesNamed,
  groups,
  modesOf,
  preferredOf,
  spec,
  specByName,
  suitesOf,
} from "./spec";

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

describe("the modes an example runs in", () => {
  const express = specByName("express");
  const transforms = specByName("with-transforms");

  it("is the full one alone for a row that names none", () => {
    expect(modesOf(transforms, ["full", "hello"])).toEqual(["full"]);
  });

  it("is what the row and the target both offer", () => {
    expect(modesOf(express, ["full", "hello"])).toEqual(["full", "hello"]);
    expect(modesOf(express, ["full"])).toEqual(["full"]);
  });

  it("names a cell of its own for the hello mode, so the two never share a slug", () => {
    expect(cellNameOf(express, "full")).toBe("express");
    expect(cellNameOf(express, "hello")).toBe("express-hello");
    expect(cellNamesOf(express)).toEqual(["express", "express-hello"]);
  });

  it("keeps health and static alone in the hello mode", () => {
    expect(suitesOf(express, "full")).toEqual(express.suites);
    expect(suitesOf(express, "hello")).toEqual(["health", "static"]);
  });
});

describe("the groups the spec declares", () => {
  it("prefers an example that carries the group", () => {
    for (const group of groups) {
      const members = spec.filter((row) => row.group === group.name).map((row) => row.name);
      expect(members).toContain(group.preferred);
      expect(preferredOf(group.name)).toBe(group.preferred);
    }
  });
});
