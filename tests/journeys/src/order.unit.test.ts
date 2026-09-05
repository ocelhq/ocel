import { describe, expect, it } from "bun:test";
import { longestFirst } from "./order";
import { type Cell, cellsOf, spec, specForTarget } from "./spec";

function cells(target: "aws" | "vps"): Cell[] {
  return specForTarget(target).flatMap((fixture) => cellsOf(fixture, target));
}

const bare: Cell[] = spec.map((fixture) => ({ name: fixture.dir, fixture }));

describe("longestFirst", () => {
  it("runs the lifecycle cells first, then the deploy cells, then the sdk ones", () => {
    const concerns = longestFirst(cells("aws")).map((cell) => cell.fixture.concern);
    expect(concerns.indexOf("deploy")).toBeGreaterThan(concerns.lastIndexOf("lifecycle"));
    expect(concerns.indexOf("sdk")).toBeGreaterThan(concerns.lastIndexOf("deploy"));
  });

  it("runs the ladders, then the composites, then the workspace inside a concern", () => {
    const kinds = longestFirst(cells("aws"))
      .filter((cell) => cell.fixture.concern === "sdk")
      .map((cell) => cell.fixture.kind);
    const firstComposite = kinds.indexOf("composite");
    const firstWorkspace = kinds.indexOf("workspace");
    expect(kinds.slice(0, firstComposite).every((kind) => kind === "ladder")).toBe(true);
    expect(kinds.slice(firstComposite, firstWorkspace).every((kind) => kind === "composite")).toBe(
      true,
    );
    expect(kinds.slice(firstWorkspace).every((kind) => kind === "workspace")).toBe(true);
  });

  it("holds the spec order inside a rank", () => {
    expect(
      longestFirst(bare)
        .filter((cell) => cell.fixture.concern === "deploy" && cell.fixture.kind === "composite")
        .map((cell) => cell.name),
    ).toEqual(["deploy/node", "deploy/next"]);
  });

  it("leaves the cells it was handed alone", () => {
    const rows = cells("vps");
    longestFirst(rows);
    expect(rows.map((cell) => cell.name)).toEqual(cells("vps").map((cell) => cell.name));
  });
});
