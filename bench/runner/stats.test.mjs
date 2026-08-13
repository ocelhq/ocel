import { describe, expect, it } from "vitest";

import { percentile, summarize } from "./stats.mjs";

describe("summarize", () => {
  it("reports nothing rather than NaN for no samples", () => {
    expect(summarize([])).toEqual({
      n: 0,
      mean: null,
      stddev: null,
      min: null,
      p50: null,
      p90: null,
      p99: null,
      max: null,
    });
    expect(summarize(undefined).n).toBe(0);
  });

  it("gives a single sample a zero spread, not a NaN one", () => {
    const stats = summarize([42]);
    expect(stats).toMatchObject({ n: 1, mean: 42, stddev: 0, min: 42, p50: 42, p99: 42, max: 42 });
    expect(Number.isNaN(stats.stddev)).toBe(false);
  });

  it("summarizes unsorted samples", () => {
    const stats = summarize([5, 1, 3, 2, 4]);
    expect(stats).toMatchObject({ n: 5, mean: 3, min: 1, max: 5, p50: 3 });
    expect(stats.stddev).toBeCloseTo(1.5811, 3);
  });

  it("drops samples that are not finite numbers", () => {
    expect(summarize([1, null, undefined, Number.NaN, Infinity, 3]).n).toBe(2);
  });

  it("takes percentiles by nearest rank", () => {
    const values = Array.from({ length: 100 }, (_, i) => i + 1);
    const stats = summarize(values);
    expect(stats.p50).toBe(50);
    expect(stats.p90).toBe(90);
    expect(stats.p99).toBe(99);
    expect(stats.max).toBe(100);
  });

  it("never interpolates between two samples", () => {
    expect(summarize([10, 20]).p50).toBe(10);
    expect(summarize([10, 20]).p90).toBe(20);
  });
});

describe("percentile", () => {
  it("clamps to the ends", () => {
    expect(percentile([1, 2, 3], 0)).toBe(1);
    expect(percentile([1, 2, 3], 100)).toBe(3);
    expect(percentile([], 50)).toBe(null);
  });
});
