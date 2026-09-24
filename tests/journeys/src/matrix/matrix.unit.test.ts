import { describe, expect, it } from "bun:test";
import { NO_FILTER, plan } from "../plan";
import { hasReleaseCycle, targetNamed } from "../targets";
import { fixtures } from "./fixtures";
import { gaps } from "./gaps";
import { LANES, type Lane, targetOfLane } from "./types";

function planOn(lane: Lane) {
  const releaseCycle = hasReleaseCycle(targetNamed(targetOfLane(lane)));
  return plan({ fixtures, gaps, lane, releaseCycle, filter: NO_FILTER });
}

describe("the journey matrix", () => {
  it("plans on every lane without a dead gap", () => {
    for (const lane of LANES) {
      expect(() => planOn(lane)).not.toThrow();
    }
  });
});

describe("the registry variant", () => {
  it("deploys through the registry on a real box", () => {
    expect(planOn("vps").cells.map((cell) => cell.name)).toContain("deploy/node-registry");
  });

  it("is skipped on the incus box every push brings up", () => {
    const incus = planOn("vps.incus");
    expect(incus.cells.map((cell) => cell.name)).not.toContain("deploy/node-registry");
    expect(Object.keys(incus.skipped)).toContain("deploy/node-registry");
  });
});
