import { describe, expect, it } from "bun:test";
import { evidence } from "./evidence";
import { cellEnv, SUPPRESS_RESOURCES_ENV } from "./ocel";
import { type Compute, type Edge, type Mode, specByName } from "./spec";
import type { CellContext } from "./targets/types";

function cell(mode: Mode, compute: Compute, edge?: Edge): CellContext {
  const example = specByName("express");
  return {
    example,
    name: "express",
    mode,
    compute,
    ...(edge === undefined ? {} : { edge }),
    suites: example.suites,
    dir: "/nowhere",
    slug: "j-1-express",
    runId: "1",
    evidence: evidence("/nowhere"),
  };
}

describe("cellEnv", () => {
  it("suppresses the resources of a hello cell alone, and carries nothing else", () => {
    expect(cellEnv(cell("hello", "serverless"))).toEqual({ [SUPPRESS_RESOURCES_ENV]: "1" });
    expect(cellEnv(cell("full", "container", "cloudflare"))).toEqual({});
  });
});
