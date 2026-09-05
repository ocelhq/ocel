import { describe, expect, it } from "bun:test";
import { longestFirst } from "./order";
import { type Cell, cellsOf, spec, specForTarget } from "./spec";

function cells(target: "aws" | "vps"): Cell[] {
  return specForTarget(target).flatMap((example) => cellsOf(example, target));
}

describe("longestFirst", () => {
  it("runs the ladders, then the composites, then the workspace", () => {
    const kinds = longestFirst(cells("aws")).map((cell) => cell.example.kind);
    const firstComposite = kinds.indexOf("composite");
    const firstWorkspace = kinds.indexOf("workspace");
    expect(kinds.slice(0, firstComposite).every((kind) => kind === "ladder")).toBe(true);
    expect(kinds.slice(firstComposite, firstWorkspace).every((kind) => kind === "composite")).toBe(true);
    expect(kinds.slice(firstWorkspace).every((kind) => kind === "workspace")).toBe(true);
  });

  it("holds the spec order inside a rank", () => {
    expect(
      longestFirst(spec.map((example) => ({ name: example.name, example })))
        .filter((cell) => cell.example.kind === "composite")
        .map((cell) => cell.name),
    ).toEqual(["express", "hono", "fastify", "next"]);
  });

  it("leaves the cells it was handed alone", () => {
    const rows = cells("vps");
    longestFirst(rows);
    expect(rows.map((cell) => cell.name)).toEqual(cells("vps").map((cell) => cell.name));
  });
});
