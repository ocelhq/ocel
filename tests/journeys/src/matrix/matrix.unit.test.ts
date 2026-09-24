import { describe, expect, it } from "bun:test";
import { NO_FILTER, plan } from "../plan";
import { hasReleaseCycle, targetNamed } from "../targets";
import { fixtures } from "./fixtures";
import { gaps } from "./gaps";
import { LANES, type Lane, targetOfLane } from "./types";

const HELD = {
  OCEL_JOURNEY_REGISTRY_USER: "octocat",
  OCEL_JOURNEY_REGISTRY_TOKEN: "ghs_t0ken",
};

function planOn(lane: Lane, env: NodeJS.ProcessEnv = {}) {
  const releaseCycle = hasReleaseCycle(targetNamed(targetOfLane(lane)));
  return plan({ fixtures, gaps, lane, releaseCycle, filter: NO_FILTER, env });
}

describe("the journey matrix", () => {
  it("plans on every lane without a dead gap, with the registry held and without", () => {
    for (const lane of LANES) {
      expect(() => planOn(lane)).not.toThrow();
      expect(() => planOn(lane, HELD)).not.toThrow();
    }
  });
});

describe("the registry variant", () => {
  it("deploys through the registry on either box when the run holds its credentials", () => {
    for (const lane of ["vps", "vps.incus"] as const) {
      expect(planOn(lane, HELD).cells.map((cell) => cell.name)).toContain("deploy/node-registry");
    }
  });

  it("is skipped on either box when the run lacks the user or the token it pushes with", () => {
    const { OCEL_JOURNEY_REGISTRY_USER: _user, ...tokenOnly } = HELD;
    const { OCEL_JOURNEY_REGISTRY_TOKEN: _token, ...userOnly } = HELD;
    for (const lane of ["vps", "vps.incus"] as const) {
      for (const env of [{}, tokenOnly, userOnly]) {
        const planned = planOn(lane, env);
        expect(planned.cells.map((cell) => cell.name)).not.toContain("deploy/node-registry");
        expect(planned.skipped["deploy/node-registry"]?.map((gap) => gap.id)).toEqual([
          "no-registry-credentials",
        ]);
      }
    }
  });
});
