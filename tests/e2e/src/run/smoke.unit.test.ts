import { describe, expect, it } from "bun:test";
import { fixtures } from "../matrix/fixtures";
import { gaps } from "../matrix/gaps";
import type { Lane } from "../matrix/types";
import { plan } from "../plan";
import { SMOKES, smokeFilter, smokeNamed } from "./smoke";

const LANE_IN_CI: Record<string, Lane> = {
  aws: "aws.floci",
  gcp: "gcp.floci",
  vps: "vps.incus",
  dev: "dev",
  next: "aws",
};

describe("a smoke", () => {
  for (const [name, smoke] of Object.entries(SMOKES)) {
    it(`${name} plans exactly one cell on the lane CI runs it on, and the gap list skips none of it`, () => {
      const planned = plan({
        fixtures,
        gaps,
        lane: LANE_IN_CI[name],
        releaseCycle: smoke.target !== "dev",
        previews: false,
        filter: smokeFilter(smoke),
        env: {},
      });
      expect(planned.cells.map((cell) => [cell.fixture, cell.variant])).toEqual([
        [smoke.fixture, smoke.variant],
      ]);
      expect(planned.skipped).toEqual({});
    });
  }

  it("keeps only the next cell deployed, for the pull request's close to reap", () => {
    expect(
      Object.entries(SMOKES)
        .filter(([, smoke]) => smoke.keep)
        .map(([name]) => name),
    ).toEqual(["next"]);
  });

  it("is refused by a name no smoke has", () => {
    expect(() => smokeNamed("azure")).toThrow("no smoke named azure");
    expect(() => smokeNamed(undefined)).toThrow("no smoke named (none)");
  });
});
