import { describe, expect, it } from "bun:test";
import {
  cellNamed,
  ENVIRONMENT_ENV,
  environmentFrom,
  selectionFor,
  SKIPS_ENV,
  skipsLifted,
  VARIANTS_ENV,
  variantsAsked,
} from "./selection";
import { specForTarget } from "./spec";
import { targetNamed } from "./targets";

const AWS = targetNamed("aws");
const VPS = targetNamed("vps");

const FULL = { OCEL_JOURNEY_COVERAGE: "full" };

function cellsOf(env: NodeJS.ProcessEnv, environment: "aws" | "aws.floci" | "vps" = "aws"): string[] {
  const target = environment === "vps" ? VPS : AWS;
  return selectionFor(target, environment, env).cells.map((cell) => cell.name);
}

describe("the environment a run says it is on", () => {
  it("reads the one the runner resolved", () => {
    expect(environmentFrom({ [ENVIRONMENT_ENV]: "aws.floci" })).toBe("aws.floci");
  });

  it("refuses to guess", () => {
    expect(() => environmentFrom({})).toThrow(/OCEL_JOURNEY_ENVIRONMENT is ""/);
    expect(() => environmentFrom({ [ENVIRONMENT_ENV]: "gcp" })).toThrow(/aws, aws.floci, dev/);
  });
});

describe("the variants a run narrows to", () => {
  const rows = specForTarget("aws");

  it("narrows to nothing when nothing is named", () => {
    expect(variantsAsked(rows, "aws", {})).toBeUndefined();
    expect(variantsAsked(rows, "aws", { [VARIANTS_ENV]: " , " })).toBeUndefined();
  });

  it("takes the names it is handed, base among them", () => {
    expect(variantsAsked(rows, "aws", { [VARIANTS_ENV]: "base cloudflare" })).toEqual(
      new Set(["base", "cloudflare"]),
    );
  });

  it("refuses a variant no example runs on this target", () => {
    expect(() => variantsAsked(rows, "aws", { [VARIANTS_ENV]: "fastly" })).toThrow(
      /aws runs no variant named fastly \(base, hello, container/,
    );
    expect(() => variantsAsked(specForTarget("vps"), "vps", { [VARIANTS_ENV]: "container" })).toThrow(
      /vps runs no variant named container/,
    );
  });
});

describe("what a run selects", () => {
  it("runs every cell that is not skipped under full coverage", () => {
    expect(cellsOf({ ...FULL, OCEL_EXAMPLES: "express", [SKIPS_ENV]: "run" })).toEqual([
      "express",
      "express-hello",
      "express-container",
      "express-api-gateway",
      "express-cloudflare",
      "express-hello-api-gateway",
    ]);
    expect(cellsOf({ ...FULL, OCEL_EXAMPLES: "express" })).toEqual(["express-hello-api-gateway"]);
  });

  it("enumerates only the variants named", () => {
    const named = { ...FULL, OCEL_EXAMPLES: "express", [SKIPS_ENV]: "run" };
    expect(cellsOf({ ...named, [VARIANTS_ENV]: "cloudflare,hello" })).toEqual([
      "express-hello",
      "express-cloudflare",
    ]);
    expect(cellsOf({ ...named, [VARIANTS_ENV]: "base" })).toEqual(["express"]);
  });

  it("reports the skipped cells it would have run, and no others", () => {
    const { skipped } = selectionFor(AWS, "aws", { ...FULL, OCEL_EXAMPLES: "with-transforms" });
    expect(Object.keys(skipped)).toEqual([
      "with-transforms",
      "with-transforms-container",
      "with-transforms-cloudflare",
    ]);
    expect(skipped["with-transforms"]?.map((gap) => gap.issue)).toEqual([923]);
    const narrowed = selectionFor(AWS, "aws", {
      ...FULL,
      OCEL_EXAMPLES: "with-transforms",
      [VARIANTS_ENV]: "api-gateway",
    });
    expect(narrowed.skipped).toEqual({});
  });

  it("covers a group with the cells that are left after the skips", () => {
    const covering = cellsOf({ OCEL_JOURNEY_SEED: "7" }, "vps");
    expect(covering.every((name) => name.endsWith("-hello"))).toBe(true);
    expect(covering.length).toBeGreaterThan(0);
  });

  it("finds a selected cell by name, and refuses one it did not select", () => {
    const selection = selectionFor(AWS, "aws", { ...FULL, OCEL_EXAMPLES: "express" });
    expect(cellNamed(selection, "express-hello-api-gateway").example.name).toBe("express");
    expect(() => cellNamed(selection, "express")).toThrow(
      /this run selects no cell named express \(express-hello-api-gateway\)/,
    );
  });
});

describe("lifting the skips", () => {
  it("keeps them unless told to run", () => {
    expect(skipsLifted({})).toBe(false);
    expect(skipsLifted({ [SKIPS_ENV]: "skip" })).toBe(false);
    expect(skipsLifted({ [SKIPS_ENV]: "run" })).toBe(true);
  });

  it("refuses a word it does not know", () => {
    expect(() => skipsLifted({ [SKIPS_ENV]: "maybe" })).toThrow(/OCEL_JOURNEY_SKIPS is maybe/);
  });
});
