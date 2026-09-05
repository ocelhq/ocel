import { describe, expect, it } from "bun:test";
import {
  cellNamed,
  CONCERN_ENV,
  concernsFrom,
  ENVIRONMENT_ENV,
  environmentFrom,
  FIXTURES_ENV,
  fixturesFor,
  selectionFor,
  SKIPS_ENV,
  skipsLifted,
  VARIANTS_ENV,
  variantsAsked,
} from "./selection";
import { fixtureNameOf, specForTarget } from "./spec";
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

describe("the concerns a run covers", () => {
  it("covers every concern when nothing names one", () => {
    expect(concernsFrom({})).toEqual(["deploy", "sdk"]);
  });

  it("covers only the bucket it is handed", () => {
    expect(concernsFrom({ [CONCERN_ENV]: "deploy" })).toEqual(["deploy"]);
  });

  it("refuses a bucket that is no concern", () => {
    expect(() => concernsFrom({ [CONCERN_ENV]: "edge" })).toThrow(/edge is no concern/);
  });

  it("offers only the named bucket's fixtures to a target", () => {
    const deploy = fixturesFor("aws", { [CONCERN_ENV]: "deploy" });
    expect(deploy.every((row) => row.concern === "deploy")).toBe(true);
    expect(deploy.map(fixtureNameOf)).toContain("deploy/next");
    expect(fixturesFor("aws", {}).length).toBe(specForTarget("aws").length);
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

  it("refuses a variant no fixture lists anywhere", () => {
    expect(() => variantsAsked(rows, "aws", { [VARIANTS_ENV]: "fastly" })).toThrow(
      /no fixture lists a variant named fastly \(base, container/,
    );
  });

  it("leaves out a variant another target runs, so one list serves every lane", () => {
    expect(
      variantsAsked(specForTarget("vps"), "vps", { [VARIANTS_ENV]: "base container cloudflare" }),
    ).toEqual(new Set(["base"]));
  });
});

describe("what a run selects", () => {
  it("runs every cell that is not skipped under full coverage", () => {
    expect(cellsOf({ ...FULL, [FIXTURES_ENV]: "deploy/express", [SKIPS_ENV]: "run" })).toEqual([
      "deploy/express",
      "deploy/express-container",
      "deploy/express-api-gateway",
      "deploy/express-cloudflare",
    ]);
    expect(cellsOf({ ...FULL, [FIXTURES_ENV]: "deploy/express" })).toEqual([
      "deploy/express-api-gateway",
      "deploy/express-cloudflare",
    ]);
  });

  it("names a cell after the bucket it belongs to, so the two frameworks never collide", () => {
    const both = cellsOf({
      ...FULL,
      [FIXTURES_ENV]: "deploy/express,sdk/express",
      [SKIPS_ENV]: "run",
      [VARIANTS_ENV]: "base",
    });
    expect(both).toEqual(["deploy/express", "sdk/express"]);
  });

  it("selects only the bucket the concern names", () => {
    const deploy = cellsOf({ ...FULL, [CONCERN_ENV]: "deploy", [SKIPS_ENV]: "run" });
    expect(deploy.every((name) => name.startsWith("deploy/"))).toBe(true);
    expect(deploy.length).toBeGreaterThan(0);
  });

  it("enumerates only the variants named", () => {
    const named = { ...FULL, [FIXTURES_ENV]: "deploy/express", [SKIPS_ENV]: "run" };
    expect(cellsOf({ ...named, [VARIANTS_ENV]: "cloudflare,container" })).toEqual([
      "deploy/express-container",
      "deploy/express-cloudflare",
    ]);
    expect(cellsOf({ ...named, [VARIANTS_ENV]: "base" })).toEqual(["deploy/express"]);
  });

  it("reports the skipped cells it would have run, and no others", () => {
    const { skipped } = selectionFor(AWS, "aws", {
      ...FULL,
      [FIXTURES_ENV]: "sdk/with-transforms",
    });
    expect(Object.keys(skipped)).toEqual([
      "sdk/with-transforms",
      "sdk/with-transforms-container",
      "sdk/with-transforms-cloudflare",
    ]);
    expect(skipped["sdk/with-transforms"]?.map((gap) => gap.issue)).toEqual([923]);
    const narrowed = selectionFor(AWS, "aws", {
      ...FULL,
      [FIXTURES_ENV]: "sdk/with-transforms",
      [VARIANTS_ENV]: "api-gateway",
    });
    expect(narrowed.skipped).toEqual({});
  });

  it("covers a group with the cells that are left after the skips", () => {
    const covering = cellsOf({ OCEL_JOURNEY_SEED: "7" }, "vps");
    expect(covering.every((name) => name.startsWith("deploy/"))).toBe(true);
    expect(covering.length).toBeGreaterThan(0);
  });

  it("finds a selected cell by name, and refuses one it did not select", () => {
    const selection = selectionFor(AWS, "aws", { ...FULL, [FIXTURES_ENV]: "deploy/express" });
    expect(cellNamed(selection, "deploy/express-cloudflare").fixture.dir).toBe("deploy/express");
    expect(() => cellNamed(selection, "deploy/express")).toThrow(
      /this run selects no cell named deploy\/express \(/,
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
