import { describe, expect, it } from "bun:test";
import { evidence } from "../evidence";
import { deploy } from "../matrix/fixtures";
import type { Variant } from "../matrix/types";
import { cloudflare, defaults } from "../matrix/variants";
import type { CellUnderTest } from "../run/cellRun";
import { cloudflareUrls } from "./cloudflare";

function cell(variant: Variant): CellUnderTest {
  return {
    fixture: deploy.node,
    name: deploy.node.name,
    variant,
    dir: "/nowhere",
    slug: "j-1-deploy-node",
    runId: "1",
    evidence: evidence("/nowhere"),
    passwordReportNonce: "journey-password-report-nonce",
  };
}

describe("cloudflareUrls", () => {
  it("reaches a cell Cloudflare fronts on its public hostname, through Cloudflare", () => {
    expect(cloudflareUrls(cell(cloudflare), "j.example")).toEqual(
      new Map([["web", "https://web-j-1-deploy-node.j.example"]]),
    );
  });

  it("leaves a cell no edge fronts to the target's own way in", () => {
    expect(cloudflareUrls(cell(defaults), "j.example")).toBeUndefined();
  });

  it("refuses a Cloudflare cell the run names no zone for", () => {
    expect(() => cloudflareUrls(cell(cloudflare), undefined)).toThrow(/OCEL_JOURNEY_ZONE/);
  });
});
