import { describe, expect, it } from "bun:test";
import { overlayFor } from "../../config";
import { evidence } from "../../evidence";
import { projectSlug } from "../../identity";
import { fixtures } from "../../matrix/fixtures";
import { cellsOn, fixturesOn } from "../../plan";
import type { CellUnderTest } from "../../run/cellRun";
import {
  cellOfSlug,
  gcpSweepOverlay,
  laneFeatures,
  previewBootstrapArgs,
  refuseFlociWithoutFirestore,
} from "./index";

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
        journeyNonce: "journey-nonce",
      };
      const overlay = gcpSweepOverlay(cell, slug, env);
      expect(overlay).toEqual(overlayFor(deployed, "gcp", env));
      expect(overlay.slug).not.toBe(slug);
    }
  });
});

describe("gcpSweepOverlay for a fronted cell", () => {
  const env = {
    OCEL_NAMESPACE: "ocel-nightly",
    OCEL_E2E_ZONE: "j.example",
  } as NodeJS.ProcessEnv;

  it("destroys a fronted cell with the DNS and hostnames it was deployed with when the run names a zone", () => {
    const fronted = cells.filter((cell) =>
      ["alb", "cloudflare"].includes(cell.variant.config.edge ?? ""),
    );
    expect(fronted).not.toEqual([]);
    for (const cell of fronted) {
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
        journeyNonce: "journey-nonce",
      };
      const overlay = gcpSweepOverlay(cell, slug, env);
      expect(overlay).toEqual(overlayFor(deployed, "gcp", env));
      expect(overlay.dns).toBe("cloudflare");
    }
  });
});

describe("laneFeatures", () => {
  const zoned = {
    OCEL_E2E_ZONE: "j.example",
    CLOUDFLARE_API_TOKEN: "token",
    CLOUDFLARE_ACCOUNT_ID: "account",
  };

  it("bootstraps the load balancer on the real lane only when the run names a zone and a Cloudflare token and account", () => {
    expect(laneFeatures({}, false)).toEqual(["private-network", "tasks"]);
    expect(laneFeatures(zoned, false)).toEqual(["private-network", "tasks", "alb-edge"]);
    expect(laneFeatures({ ...zoned, CLOUDFLARE_API_TOKEN: " " }, false)).toEqual([
      "private-network",
      "tasks",
    ]);
  });

  it("bootstraps tasks alone on floci", () => {
    expect(laneFeatures({}, true)).toEqual(["tasks"]);
    expect(laneFeatures(zoned, true)).toEqual(["tasks"]);
  });
});

describe("previewBootstrapArgs", () => {
  const zoned = {
    OCEL_E2E_ZONE: "j.example",
    CLOUDFLARE_API_TOKEN: "token",
    CLOUDFLARE_ACCOUNT_ID: "account",
  };
  const previewing = { cells: [{ phases: ["deploy", "verify", "preview", "destroy"] }] };
  const plain = { cells: [{ phases: ["deploy", "verify", "destroy"] }] };

  it("bootstraps the preview tier with tasks and the load balancer when a planned cell previews and the zone is set", () => {
    expect(previewBootstrapArgs(zoned, false, previewing)).toEqual([
      "bootstrap",
      "preview",
      "--yes",
      "--features",
      "tasks,alb-edge",
    ]);
    expect(previewBootstrapArgs({}, false, previewing)).toEqual([
      "bootstrap",
      "preview",
      "--yes",
      "--features",
      "tasks",
    ]);
  });

  it("bootstraps no preview tier on floci or when no planned cell previews", () => {
    expect(previewBootstrapArgs(zoned, true, previewing)).toBeUndefined();
    expect(previewBootstrapArgs(zoned, false, plain)).toBeUndefined();
  });
});

describe("refuseFlociWithoutFirestore", () => {
  it("refuses the floci lane when floci runs without the Firestore emulator beside it", () => {
    const refused = refuseFlociWithoutFirestore({
      OCEL_FLOCI_GCP_ENDPOINT: "http://127.0.0.1:4588",
    });

    expect(refused).toBeInstanceOf(Error);
    expect(refused?.message).toContain("OCEL_FLOCI_FIRESTORE_ENDPOINT");
    expect(refused?.message).toContain("scripts/floci.sh --cloud gcp");
  });

  it("accepts the floci lane with the Firestore emulator beside it", () => {
    expect(
      refuseFlociWithoutFirestore({
        OCEL_FLOCI_GCP_ENDPOINT: "http://127.0.0.1:4588",
        OCEL_FLOCI_FIRESTORE_ENDPOINT: "http://127.0.0.1:8085",
      }),
    ).toBeUndefined();
  });

  it("leaves a lane on a real project alone", () => {
    expect(refuseFlociWithoutFirestore({ OCEL_GCP_PROJECT: "acme" })).toBeUndefined();
  });
});
