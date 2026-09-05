import { describe, expect, it } from "bun:test";
import {
  cellNameOf,
  cellsOf,
  type ExampleSpec,
  examplesNamed,
  groups,
  preferredOf,
  spec,
  specByName,
  suitesOf,
  variantNameOf,
  variantsOf,
} from "./spec";
import { apiGateway, cloudflare, container, hello, helloApiGateway } from "./variants";

const rows: ExampleSpec[] = [
  { name: "express", dir: "express", framework: "express", kind: "composite", suites: [], apps: [] },
  { name: "hono", dir: "hono", framework: "hono", kind: "composite", suites: [], apps: [] },
];

function names(example: ExampleSpec, target: "aws" | "vps" | "dev"): string[] {
  return cellsOf(example, target).map((cell) => cell.name);
}

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

describe("the variants an example lists", () => {
  const express = specByName("express");
  const transforms = specByName("with-transforms");

  it("is none for a row that lists none", () => {
    expect(variantsOf(rows[0] as ExampleSpec, "aws")).toEqual([]);
    expect(names(rows[0] as ExampleSpec, "aws")).toEqual(["express"]);
  });

  it("is what the row lists and the target runs", () => {
    expect(variantsOf(express, "aws")).toEqual([
      hello,
      container,
      apiGateway,
      cloudflare,
      helloApiGateway,
    ]);
    expect(variantsOf(express, "vps")).toEqual([hello]);
    expect(variantsOf(express, "dev")).toEqual([]);
    expect(variantsOf(transforms, "aws")).toEqual([container, apiGateway, cloudflare]);
  });

  it("refuses a row that lists one variant twice", () => {
    const twice: ExampleSpec = { ...express, variants: [hello, container, hello] };
    expect(() => variantsOf(twice, "aws")).toThrow(/lists the hello variant twice/);
  });

  it("names the base cell after the example and a variant's cell after both", () => {
    expect(cellNameOf(express, undefined)).toBe("express");
    expect(cellNameOf(express, hello)).toBe("express-hello");
    expect(cellNameOf(specByName("workspace"), helloApiGateway)).toBe(
      "workspace-hello-api-gateway",
    );
  });

  it("lists the base cell first, then one cell per variant in the order listed", () => {
    expect(names(express, "aws")).toEqual([
      "express",
      "express-hello",
      "express-container",
      "express-api-gateway",
      "express-cloudflare",
      "express-hello-api-gateway",
    ]);
    expect(names(express, "vps")).toEqual(["express", "express-hello"]);
    expect(names(express, "dev")).toEqual(["express"]);
  });

  it("calls the base cell's variant base", () => {
    expect(cellsOf(express, "aws").map(variantNameOf)).toEqual([
      "base",
      "hello",
      "container",
      "api-gateway",
      "cloudflare",
      "hello-api-gateway",
    ]);
  });

  it("keeps only the suites a variant keeps", () => {
    expect(suitesOf(express, undefined)).toEqual(express.suites);
    expect(suitesOf(express, hello)).toEqual(["health", "static"]);
    expect(suitesOf(express, container)).toEqual(express.suites);
    expect(suitesOf(express, helloApiGateway)).toEqual(["health", "static"]);
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
