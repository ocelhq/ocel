import { mkdirSync, rmSync, writeFileSync } from "node:fs";
import path from "node:path";
import { afterAll, describe, expect, it } from "bun:test";
import { outputRoot, prepareFile } from "./paths";
import { failureFor, readPrepared, readPrepareFailures } from "./prepare";

describe("the failure a cell reads out of a prepared lane", () => {
  it("leaves every cell alone when the lane prepared cleanly", () => {
    expect(failureFor({}, "cloudflare")).toBeUndefined();
    expect(failureFor({}, undefined)).toBeUndefined();
  });

  it("blocks only the cells on the edge that failed", () => {
    const failures = { edges: { cloudflare: "CLOUDFLARE_ACCOUNT_ID is not set" } };
    expect(failureFor(failures, "cloudflare")).toBe("CLOUDFLARE_ACCOUNT_ID is not set");
    expect(failureFor(failures, "cloudfront")).toBeUndefined();
    expect(failureFor(failures, "api-gateway")).toBeUndefined();
    expect(failureFor(failures, undefined)).toBeUndefined();
  });

  it("blocks every cell when the failure precedes the edges", () => {
    const failures = { lane: "the emulator never showed a default VPC" };
    expect(failureFor(failures, "cloudfront")).toBe(failures.lane);
    expect(failureFor(failures, undefined)).toBe(failures.lane);
  });
});

describe("what a cell reads back out of the file the lane prepared", () => {
  const runId = "unit-prepare";
  const target = "dev";
  const runDir = path.join(outputRoot, runId);

  afterAll(() => {
    rmSync(runDir, { recursive: true, force: true });
  });

  function write(body: string) {
    const file = prepareFile(runId, target);
    mkdirSync(path.dirname(file), { recursive: true });
    writeFileSync(file, body, "utf8");
  }

  it("carries the failures and the duration the lane wrote", () => {
    const failures = { edges: { cloudflare: "CLOUDFLARE_ACCOUNT_ID is not set" } };
    write(`${JSON.stringify({ ms: 4_200, failures })}\n`);
    expect(readPrepared(runId, target)).toEqual({ ms: 4_200, failures });
    expect(readPrepareFailures(runId, target)).toEqual(failures);
  });

  it("reads a clean lane as no failure at all", () => {
    write(`${JSON.stringify({ ms: 12, failures: {} })}\n`);
    expect(readPrepareFailures(runId, target)).toEqual({});
  });

  it("reads a lane that wrote nothing as no failure, so a cell still runs", () => {
    rmSync(runDir, { recursive: true, force: true });
    expect(readPrepared(runId, target)).toBeUndefined();
    expect(readPrepareFailures(runId, target)).toEqual({});
  });
});
