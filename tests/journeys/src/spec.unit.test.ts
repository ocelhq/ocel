import { describe, expect, it } from "vitest";
import {
  cellNameOf,
  cellNamesOf,
  type ExampleSpec,
  examplesNamed,
  groups,
  modesOf,
  type Offered,
  preferredOf,
  spec,
  specByName,
  suitesOf,
  variantsOf,
} from "./spec";
import { targetNamed } from "./targets";

const AWS: Offered = {
  modes: ["full", "hello"],
  computes: ["serverless", "container"],
  edges: ["cloudfront", "api-gateway", "cloudflare"],
};
const BOX: Offered = { modes: ["full", "hello"], computes: ["container"], edges: [] };

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
    expect(cellNameOf(express, { mode: "full", compute: "serverless", edge: "cloudfront" }, AWS)).toBe(
      "express",
    );
    expect(cellNameOf(express, { mode: "hello", compute: "serverless", edge: "cloudfront" }, AWS)).toBe(
      "express-hello",
    );
  });

  it("keeps health and static alone in the hello mode", () => {
    expect(suitesOf(express, "full")).toEqual(express.suites);
    expect(suitesOf(express, "hello")).toEqual(["health", "static"]);
  });
});

describe("the variants an example runs in", () => {
  const express = specByName("express");
  const transforms = specByName("with-transforms");

  it("pairs every mode the row runs with every edge and compute the target offers", () => {
    expect(variantsOf(express, BOX)).toEqual([
      { mode: "full", compute: "container" },
      { mode: "hello", compute: "container" },
    ]);
    expect(variantsOf(transforms, AWS)).toEqual([
      { mode: "full", compute: "serverless", edge: "cloudfront" },
      { mode: "full", compute: "container", edge: "cloudfront" },
      { mode: "full", compute: "serverless", edge: "api-gateway" },
      { mode: "full", compute: "container", edge: "api-gateway" },
      { mode: "full", compute: "serverless", edge: "cloudflare" },
      { mode: "full", compute: "container", edge: "cloudflare" },
    ]);
  });

  it("names a cell of its own for a compute or an edge the target does not name first", () => {
    expect(cellNameOf(express, { mode: "full", compute: "container", edge: "cloudfront" }, AWS)).toBe(
      "express-container",
    );
    expect(cellNameOf(express, { mode: "full", compute: "serverless", edge: "cloudflare" }, AWS)).toBe(
      "express-cloudflare",
    );
  });

  it("orders the suffixes mode, edge, compute, so a slug reads the same every run", () => {
    expect(
      cellNameOf(express, { mode: "hello", compute: "container", edge: "api-gateway" }, AWS),
    ).toBe("express-hello-api-gateway-container");
    expect(
      cellNameOf(specByName("workspace"), { mode: "hello", compute: "container", edge: "api-gateway" }, AWS),
    ).toBe("workspace-hello-api-gateway-container");
  });

  it("names the target's first compute and first edge in no cell name", () => {
    expect(cellNameOf(express, { mode: "full", compute: "container" }, BOX)).toBe("express");
    expect(cellNamesOf(express, BOX)).toEqual(["express", "express-hello"]);
  });

  it("lists every cell name the target's variants make", () => {
    expect(cellNamesOf(express, AWS)).toEqual([
      "express",
      "express-container",
      "express-api-gateway",
      "express-api-gateway-container",
      "express-cloudflare",
      "express-cloudflare-container",
      "express-hello",
      "express-hello-container",
      "express-hello-api-gateway",
      "express-hello-api-gateway-container",
      "express-hello-cloudflare",
      "express-hello-cloudflare-container",
    ]);
  });

  it("leaves the cells of a target that offers no edge untouched", () => {
    for (const name of ["vps", "dev"] as const) {
      const target = targetNamed(name);
      expect(target.edges).toEqual([]);
      expect(cellNamesOf(express, target).every((cell) => !cell.includes("cloud"))).toBe(true);
    }
    expect(cellNamesOf(express, targetNamed("vps"))).toEqual(["express", "express-hello"]);
    expect(cellNamesOf(express, targetNamed("dev"))).toEqual(["express"]);
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
