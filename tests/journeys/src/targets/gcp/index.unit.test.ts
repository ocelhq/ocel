import { describe, expect, it } from "bun:test";
import { overlayFor } from "../../config";
import { evidence } from "../../evidence";
import { projectSlug } from "../../identity";
import { fixtures } from "../../matrix/fixtures";
import { cellsOn, fixturesOn } from "../../plan";
import type { CellUnderTest } from "../../run/cellRun";
import { cellOfSlug, gcpSweepOverlay } from "./index";

const cells = fixturesOn(fixtures, "gcp").flatMap((one) => cellsOn(one, "gcp"));

describe("cellOfSlug", () => {
  it("reads back the cell a slug was made for, so a sweep knows which apps it deploys", () => {
    expect(cellOfSlug(cells, "j-1874-deploy-node").name).toBe("deploy/node");
  });

  it("takes the longest name a slug ends in, not the one it merely ends with", () => {
    expect(cellOfSlug(cells, "j-1874-deploy-node-container").name).toBe("deploy/node-container");
  });

  it("refuses a slug no cell of this target owns", () => {
    expect(() => cellOfSlug(cells, "j-1874-deploy-elsewhere")).toThrow(/names no cell/);
  });
});

describe("gcpSweepOverlay", () => {
  const env = { OCEL_NAMESPACE: "ocel-nightly" } as NodeJS.ProcessEnv;

  it("names the deploy the slug it was deployed under, not the one the run id spells", () => {
    for (const cell of cells) {
      const runId = "18746093211";
      const slug = projectSlug(cell.name, runId);
      const deployed: CellUnderTest = {
        name: cell.name,
        fixture: cell.fixture,
        variant: cell.variant,
        dir: "/nowhere",
        slug,
        runId,
        evidence: evidence("/nowhere"),
      };
      const overlay = gcpSweepOverlay(cell, slug, env);
      expect(overlay).toEqual(overlayFor(deployed, "gcp", env));
      expect(overlay.slug).not.toBe(slug);
    }
  });
});
