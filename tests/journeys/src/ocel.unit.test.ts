import { describe, expect, it } from "bun:test";
import { evidence } from "./evidence";
import { cellEnv } from "./ocel";
import { specByName } from "./spec";
import type { CellContext } from "./targets/types";
import { container, hello, helloApiGateway, SUPPRESS_RESOURCES_ENV, type Variant } from "./variants";

function cell(variant?: Variant): CellContext {
  const example = specByName("express");
  return {
    example,
    name: "express",
    ...(variant === undefined ? {} : { variant }),
    suites: example.suites,
    dir: "/nowhere",
    slug: "j-1-express",
    runId: "1",
    evidence: evidence("/nowhere"),
  };
}

describe("cellEnv", () => {
  it("carries the variant's environment, and nothing for a base cell", () => {
    expect(cellEnv(cell(hello))).toEqual({ [SUPPRESS_RESOURCES_ENV]: "1" });
    expect(cellEnv(cell(helloApiGateway))).toEqual({ [SUPPRESS_RESOURCES_ENV]: "1" });
    expect(cellEnv(cell(container))).toEqual({});
    expect(cellEnv(cell())).toEqual({});
  });
});
