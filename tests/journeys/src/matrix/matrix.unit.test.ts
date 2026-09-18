import { describe, expect, it } from "bun:test";
import { EVERYTHING, plan } from "../plan";
import { targetNamed } from "../targets";
import { fixtures } from "./fixtures";
import { gaps } from "./gaps";
import { LANES, targetOfLane } from "./types";

describe("the journey matrix", () => {
  it("plans on every lane without a dead gap", () => {
    for (const lane of LANES) {
      const legs = targetNamed(targetOfLane(lane)).legs;
      expect(() => plan({ fixtures, gaps, lane, legs, ask: EVERYTHING })).not.toThrow();
    }
  });
});
