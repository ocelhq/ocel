import { mkdirSync, rmSync, writeFileSync } from "node:fs";
import path from "node:path";
import { afterAll, describe, expect, it } from "bun:test";
import { outputRoot, prepareFile } from "./paths";
import { readPrepared, readPrepareFailure } from "./prepare";

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

  it("carries the failure and the duration the lane wrote", () => {
    const failures = { lane: "the emulator never showed a default VPC" };
    write(`${JSON.stringify({ ms: 4_200, failures })}\n`);
    expect(readPrepared(runId, target)).toEqual({ ms: 4_200, failures });
    expect(readPrepareFailure(runId, target)).toBe(failures.lane);
  });

  it("reads a clean lane as no failure at all", () => {
    write(`${JSON.stringify({ ms: 12, failures: {} })}\n`);
    expect(readPrepareFailure(runId, target)).toBeUndefined();
  });

  it("reads a lane that wrote nothing as no failure, so a cell still runs", () => {
    rmSync(runDir, { recursive: true, force: true });
    expect(readPrepared(runId, target)).toBeUndefined();
    expect(readPrepareFailure(runId, target)).toBeUndefined();
  });
});
