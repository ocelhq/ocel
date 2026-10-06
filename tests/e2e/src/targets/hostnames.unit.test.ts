import { describe, expect, it } from "bun:test";
import { evidence } from "../evidence";
import { deploy } from "../matrix/fixtures";
import type { Variant } from "../matrix/types";
import { alb, cloudflare, defaults } from "../matrix/variants";
import type { CellUnderTest } from "../run/cellRun";
import { hostnameUrls } from "./hostnames";

function cell(variant: Variant): CellUnderTest {
  return {
    fixture: deploy.node,
    name: deploy.node.name,
    variant,
    dir: "/nowhere",
    slug: "j-1-deploy-node",
    runId: "1",
    evidence: evidence("/nowhere"),
    journeyNonce: "journey-nonce",
  };
}

describe("hostnameUrls", () => {
  it("reaches a cell Cloudflare fronts on its public hostname, through Cloudflare", () => {
    expect(hostnameUrls(cell(cloudflare), "j.example")).toEqual(
      new Map([["web", "https://web-j-1-deploy-node.j.example"]]),
    );
  });

  it("leaves a cell no edge fronts to the target's own way in", () => {
    expect(hostnameUrls(cell(defaults), "j.example")).toBeUndefined();
  });

  it("refuses a Cloudflare cell the run names no zone for", () => {
    expect(() => hostnameUrls(cell(cloudflare), undefined)).toThrow(/OCEL_E2E_ZONE/);
  });

  it("reaches a cell the load balancer fronts on its public hostname", () => {
    expect(hostnameUrls(cell(alb), "j.example")).toEqual(
      new Map([["web", "https://web-j-1-deploy-node.j.example"]]),
    );
  });

  it("refuses an alb cell the run names no zone for", () => {
    expect(() => hostnameUrls(cell(alb), undefined)).toThrow(/fronted by alb.*OCEL_E2E_ZONE/);
  });
});
