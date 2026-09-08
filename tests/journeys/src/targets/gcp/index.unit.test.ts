import { describe, expect, it } from "bun:test";
import { shapeFor } from "../../config";
import { projectSlug } from "../../identity";
import { cellsOf, specForTarget } from "../../spec";
import type { CellContext } from "../types";
import { cellOfSlug, sweepOverlay } from "./index";

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

describe("sweepOverlay", () => {
  const env = { OCEL_NAMESPACE: "ocel-nightly" } as NodeJS.ProcessEnv;

  it("names the deploy the slug it was stood up under, not the one the run id spells", () => {
    for (const cell of cells) {
      const slug = projectSlug(cell.name, "18746093211");
      const overlay = sweepOverlay(cell, slug, env);
      expect(overlay).toEqual(shapeFor({ ...cell, slug } as CellContext, "gcp", env));
      expect(overlay.slug).not.toBe(slug);
    }
  });
});
