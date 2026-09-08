import { describe, expect, it } from "bun:test";
import { cellsOf, specForTarget } from "../../spec";
import { cellOfSlug } from "./index";

const cells = specForTarget("gcp").flatMap((row) => cellsOf(row, "gcp"));

describe("cellOfSlug", () => {
  it("reads back the cell a slug was made for, so a sweep knows which apps it stands", () => {
    expect(cellOfSlug(cells, "j-1874-deploy-node").name).toBe("deploy/node");
  });

  it("takes the longest name a slug ends in, not the one it merely ends with", () => {
    expect(cellOfSlug(cells, "j-1874-deploy-node-container").name).toBe("deploy/node-container");
  });

  it("refuses a slug no cell of this target owns", () => {
    expect(() => cellOfSlug(cells, "j-1874-deploy-elsewhere")).toThrow(/names no cell/);
  });
});
