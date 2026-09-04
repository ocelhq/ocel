import { describe, expect, it } from "vitest";
import { laneWorkers } from "./index";

const target = { concurrency: 3 };

describe("laneWorkers", () => {
  it("takes the target's concurrency when nothing overrides it", () => {
    expect(laneWorkers(target, {})).toBe(3);
  });

  it("takes the override when it names a positive integer", () => {
    expect(laneWorkers(target, { OCEL_JOURNEY_WORKERS: "6" })).toBe(6);
  });

  it("ignores an override that is not a positive integer", () => {
    for (const asked of ["", " ", "0", "-2", "1.5", "many"]) {
      expect(laneWorkers(target, { OCEL_JOURNEY_WORKERS: asked })).toBe(3);
    }
  });
});
