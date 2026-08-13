import { describe, expect, it } from "vitest";

import { APPS, PLATFORMS } from "../matrix.config.mjs";
import { cellId, deploymentProblems, driverProblems, expandMatrix } from "./contract.mjs";
import * as fake from "../platforms/fake/driver.mjs";

const DEPLOYMENT = Object.freeze({
  url: "https://abc.lambda-url.us-east-1.on.aws",
  functionName: "bench-express-ocel-bundle",
  buildMs: 1200,
  provisionMs: 3400,
});

describe("driverProblems", () => {
  it("accepts the fake driver, which is what --dry-run leans on", () => {
    expect(driverProblems("fake", fake)).toEqual([]);
  });

  it("names each missing export", () => {
    expect(driverProblems("sst", { deploy() {} })).toEqual(["sst exports no teardown()"]);
    expect(driverProblems("sst", {})).toHaveLength(2);
    expect(driverProblems("sst", undefined)).toHaveLength(2);
  });
});

describe("deploymentProblems", () => {
  it("accepts a complete deployment", () => {
    expect(deploymentProblems("ocel-bundle", DEPLOYMENT)).toEqual([]);
  });

  it("rejects a missing functionName, without which no cold start can be forced", () => {
    const [problem] = deploymentProblems("raw", { ...DEPLOYMENT, functionName: "" });
    expect(problem).toContain("no cold start can be forced");
  });

  it("rejects a url that is not http(s)", () => {
    expect(deploymentProblems("raw", { ...DEPLOYMENT, url: "abc.on.aws" })).toHaveLength(1);
  });

  it("rejects non-numeric timings", () => {
    expect(deploymentProblems("raw", { ...DEPLOYMENT, buildMs: null, provisionMs: "3s" })).toHaveLength(2);
  });

  it("rejects a driver that resolved nothing at all", () => {
    expect(deploymentProblems("raw", undefined)).toHaveLength(1);
  });
});

describe("expandMatrix", () => {
  it("expands the whole matrix when nothing is selected", () => {
    const cells = expandMatrix({ apps: APPS, platforms: PLATFORMS, only: {} });
    expect(cells).toHaveLength(APPS.length * PLATFORMS.length);
    expect(cells[0].id).toBe(cellId(APPS[0].name, PLATFORMS[0].id));
  });

  it("selects by framework and platform", () => {
    const cells = expandMatrix({
      apps: APPS,
      platforms: PLATFORMS,
      only: { frameworks: ["hono"], platforms: ["sst", "raw"] },
    });
    expect(cells.map((cell) => cell.id)).toEqual(["hono/sst", "hono/raw"]);
  });

  it("yields nothing for a name that is in neither list", () => {
    expect(expandMatrix({ apps: APPS, platforms: PLATFORMS, only: { frameworks: ["remix"] } })).toEqual([]);
  });
});
