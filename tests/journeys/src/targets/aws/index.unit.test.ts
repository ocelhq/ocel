import { describe, expect, it } from "bun:test";
import { type Cell, cellsOf, specByName, specForTarget } from "../../spec";
import { cellsBySlugPart } from "./index";

const fixture = specByName("deploy", "express");

function named(name: string): Cell {
  return { name, fixture };
}

describe("cellsBySlugPart", () => {
  it("keys every cell the aws lane runs, and loses none of them", () => {
    const cells = specForTarget("aws").flatMap((row) => cellsOf(row, "aws"));

    expect(cellsBySlugPart(cells).size).toBe(cells.length);
  });

  it("refuses two cells that slug to one part, rather than dropping one", () => {
    expect(() =>
      cellsBySlugPart([named("deploy/express-api-gateway"), named("deploy/express/api-gateway")]),
    ).toThrow(/both slug to deploy-express-api-gateway/);
  });
});
