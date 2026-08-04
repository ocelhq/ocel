import { describe, expect, it } from "vitest";

import { parseArgs } from "../src/options";

const parse = (...argv: string[]) => parseArgs(argv, "/default/out.json");

describe("parseArgs", () => {
  it("requires a base", () => {
    expect(() => parse()).toThrow("--base");
  });

  it("applies defaults and strips a trailing slash from the base", () => {
    expect(parse("--base", "https://probe.example.com/")).toEqual({
      base: "https://probe.example.com",
      concurrency: 64,
      rounds: 4,
      sentinelSeconds: 60,
      pollSeconds: 1,
      pollFanout: 8,
      windowSeconds: 180,
      ttls: [10],
      out: "/default/out.json",
    });
  });

  it("rejects a flag given no value rather than reading it as zero", () => {
    expect(() => parse("--base", "https://p.example.com", "--rounds")).toThrow(
      "--rounds requires a value",
    );
    expect(() => parse("--rounds", "--base", "https://p.example.com")).toThrow(
      "--rounds requires a value",
    );
  });

  it("rejects non-positive and non-numeric values", () => {
    for (const bad of ["0", "-1", "abc", ""]) {
      expect(() => parse("--base", "https://p.example.com", "--concurrency", bad)).toThrow(
        "--concurrency must be a positive number",
      );
    }
  });

  it("parses a ttl list and rejects a malformed one", () => {
    expect(parse("--base", "https://p.example.com", "--ttls", "1,5,30").ttls).toEqual([
      1, 5, 30,
    ]);
    expect(() => parse("--base", "https://p.example.com", "--ttls", "1,,5")).toThrow(
      "--ttls must be a positive number",
    );
  });

  it("rejects a positional argument", () => {
    expect(() => parse("https://p.example.com")).toThrow("unexpected argument");
  });
});
