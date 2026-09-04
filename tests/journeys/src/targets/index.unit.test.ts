import { describe, expect, it } from "vitest";
import type { Compute, TargetName } from "../spec";
import { laneWorkers, targetNamed } from "./index";

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

describe("the computes a target offers", () => {
  const defaults: Record<TargetName, Compute> = {
    aws: "serverless",
    vps: "container",
    dev: "serverless",
  };

  it("names the default first, so no cell name carries it", () => {
    for (const [name, compute] of Object.entries(defaults)) {
      expect(targetNamed(name).computes[0]).toBe(compute);
    }
  });

  it("offers each compute once, and offers at least one", () => {
    for (const name of Object.keys(defaults)) {
      const computes = targetNamed(name).computes;
      expect(computes.length).toBeGreaterThan(0);
      expect(new Set(computes).size).toBe(computes.length);
    }
  });
});
