import { describe, expect, it } from "vitest";
import { evidence } from "./evidence";
import { cellEnv, SUPPRESS_RESOURCES_ENV } from "./ocel";
import { type Compute, type Mode, specByName } from "./spec";
import type { CellContext } from "./targets/types";

function cell(mode: Mode, compute: Compute): CellContext {
  const example = specByName("express");
  return {
    example,
    name: "express",
    mode,
    compute,
    suites: example.suites,
    dir: "/nowhere",
    slug: "j-1-express",
    runId: "1",
    evidence: evidence("/nowhere"),
  };
}

describe("cellEnv", () => {
  it("carries the compute of the cell, whichever it is", () => {
    expect(cellEnv(cell("full", "serverless")).OCEL_JOURNEY_COMPUTE).toBe("serverless");
    expect(cellEnv(cell("hello", "container")).OCEL_JOURNEY_COMPUTE).toBe("container");
  });

  it("suppresses the resources of a hello cell alone", () => {
    expect(cellEnv(cell("hello", "serverless"))[SUPPRESS_RESOURCES_ENV]).toBe("1");
    expect(cellEnv(cell("full", "serverless"))[SUPPRESS_RESOURCES_ENV]).toBe(undefined);
  });
});
