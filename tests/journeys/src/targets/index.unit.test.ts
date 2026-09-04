import { describe, expect, it } from "vitest";
import { offeredBy } from "../offer";
import type { Compute, Edge, TargetName } from "../spec";
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

describe("the edges a target offers", () => {
  const defaults: Partial<Record<TargetName, Edge>> = { aws: "cloudfront" };

  it("names the default first, so no cell name carries it", () => {
    for (const [name, edge] of Object.entries(defaults)) {
      expect(targetNamed(name).edges[0]).toBe(edge);
    }
  });

  it("offers no edge where the target has none", () => {
    for (const name of ["vps", "dev"]) {
      expect(targetNamed(name).edges).toEqual([]);
    }
  });
});

describe("narrowing what a target offers", () => {
  const aws = targetNamed("aws");

  it("offers everything when nothing narrows it", () => {
    expect(offeredBy(aws, {})).toEqual({
      modes: aws.modes,
      computes: aws.computes,
      edges: aws.edges,
    });
  });

  it("keeps the target's order, so the default stays first when it survives", () => {
    expect(offeredBy(aws, { OCEL_JOURNEY_EDGES: "cloudflare cloudfront" }).edges).toEqual([
      "cloudfront",
      "cloudflare",
    ]);
    expect(offeredBy(aws, { OCEL_JOURNEY_COMPUTES: "container" }).computes).toEqual(["container"]);
  });

  it("refuses a value the target does not offer", () => {
    expect(() => offeredBy(aws, { OCEL_JOURNEY_EDGES: "fastly" })).toThrow(
      /the aws target offers no edge named fastly/,
    );
    expect(() => offeredBy(aws, { OCEL_JOURNEY_COMPUTES: "steam" })).toThrow(
      /the aws target offers no compute named steam/,
    );
  });
});
