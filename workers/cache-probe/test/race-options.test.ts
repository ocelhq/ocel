import { describe, expect, it } from "vitest";

import { parseRaceOptions } from "../src/race-options.ts";

const parse = (...argv: string[]) =>
  parseRaceOptions(["--base", "https://probe.example.com", ...argv], "/default/out.json");

describe("parseRaceOptions", () => {
  it("requires a base", () => {
    expect(() => parseRaceOptions([], "/default/out.json")).toThrow("--base");
  });

  it("applies defaults and strips a trailing slash from the base", () => {
    expect(parseRaceOptions(["--base", "https://probe.example.com/"], "/out.json")).toEqual({
      base: "https://probe.example.com",
      phase: "control",
      scope: "offzone",
      trials: 100,
      deltas: [0, 10, 25, 50, 100, 150, 200, 300, 500, 1_000],
      sizes: [2, 8, 32, 128],
      jitters: [0],
      sockets: 16,
      windowMs: null,
      isolatesPerColo: null,
      colos: 300,
      sentinelTtlSeconds: 5,
      maxDiscardRate: 0.02,
      out: "/out.json",
    });
  });

  it("defaults the gap phase to more trials than the burst", () => {
    expect(parse("--phase", "gap").trials).toBe(200);
    expect(parse("--phase", "burst").trials).toBe(100);
  });

  it("rejects a phase or scope it does not have", () => {
    expect(() => parse("--phase", "sweep")).toThrow("--phase must be control, gap or burst");
    expect(() => parse("--scope", "anyzone")).toThrow("--scope must be onzone or offzone");
  });

  it("rejects a flag given no value rather than reading it as zero", () => {
    expect(() => parse("--trials")).toThrow("--trials requires a value");
    expect(() => parse("--trials", "--sockets", "4")).toThrow("--trials requires a value");
  });

  it("names the bound it broke rather than a floating-point sentinel", () => {
    // `--trials 0` once aborted with "must be a number >= 5e-324", which reads
    // as an instrument fault rather than as a typo in the invocation.
    expect(() => parse("--trials", "0")).toThrow("--trials must be at least 1");
    expect(() => parse("--trials", "abc")).toThrow("--trials must be a number");
    expect(() => parse("--trials", "")).toThrow("--trials must be a number");
  });

  it("sweeps jitter windows and keeps zero, the un-jittered baseline, expressible", () => {
    expect(parse("--jitters", "0,250,1000").jitters).toEqual([0, 250, 1_000]);
    expect(() => parse("--jitters", "0,-1")).toThrow("--jitters must be at least 0");
    expect(() => parse("--jitters", "0,x")).toThrow("--jitters must be a number");
  });

  it("refuses a pool of one instead of quietly racing two", () => {
    // A pool of one can never produce a cross-isolate pair, so a coerced 2
    // would run a sweep the caller did not ask for and report it as theirs.
    expect(() => parse("--sockets", "1")).toThrow("--sockets must be at least 2");
    expect(parse("--sockets", "2").sockets).toBe(2);
  });

  it("requires whole counts, since a fractional burst cannot be sized", () => {
    // `--sizes 2.5` used to open 2 sockets for a trial the summary filed under
    // size 2.5, which the runner then reported as the instrument double-counting.
    expect(() => parse("--sizes", "2.5")).toThrow("--sizes must be a whole number");
    expect(() => parse("--sizes", "0")).toThrow("--sizes must be at least 1");
    expect(parse("--sizes", "2,8,32").sizes).toEqual([2, 8, 32]);
  });

  it("takes a fractional delay, because the window is milliseconds wide", () => {
    expect(parse("--deltas", "0,2.5,7.5").deltas).toEqual([0, 2.5, 7.5]);
    expect(() => parse("--deltas", "-1")).toThrow("--deltas must be at least 0");
  });

  it("takes a window of zero, which is a real answer", () => {
    expect(parse("--window", "0").windowMs).toBe(0);
    expect(parse("--window", "10").windowMs).toBe(10);
    expect(() => parse("--window", "-1")).toThrow("--window must be at least 0");
  });

  it("takes the isolate ceiling as a flag rather than inheriting a literal", () => {
    expect(parse("--isolates", "99").isolatesPerColo).toBe(99);
    expect(() => parse("--isolates", "0")).toThrow("--isolates must be at least 1");
  });

  it("bounds the discard ceiling to a fraction", () => {
    expect(parse("--maxDiscardRate", "0").maxDiscardRate).toBe(0);
    expect(() => parse("--maxDiscardRate", "2")).toThrow("--maxDiscardRate must be between 0 and 1");
  });

  it("rejects a sentinel ttl of zero, which the worker itself refuses", () => {
    expect(() => parse("--sentinelTtl", "0")).toThrow("--sentinelTtl must be greater than 0");
    expect(parse("--sentinelTtl", "0.5").sentinelTtlSeconds).toBe(0.5);
  });

  it("rejects a positional argument", () => {
    expect(() => parseRaceOptions(["https://p.example.com"], "/out.json")).toThrow(
      "unexpected argument",
    );
  });
});
