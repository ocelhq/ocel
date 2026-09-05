import { describe, expect, it } from "bun:test";
import { evidence } from "./evidence";
import { cellEnv } from "./ocel";
import { specByName } from "./spec";
import type { CellContext } from "./targets/types";
import { container, type Variant } from "./variants";

const carrying: Variant = { name: "carrying", env: { OCEL_JOURNEY_PROBE: "1" } };

function cell(variant?: Variant): CellContext {
  return {
    fixture: specByName("sdk", "express"),
    name: "sdk/express",
    ...(variant === undefined ? {} : { variant }),
    dir: "/nowhere",
    slug: "j-1-sdk-express",
    runId: "1",
    evidence: evidence("/nowhere"),
  };
}

describe("cellEnv", () => {
  it("carries the variant's environment, and nothing for a variant that names none", () => {
    expect(cellEnv(cell(carrying))).toEqual({ OCEL_JOURNEY_PROBE: "1" });
    expect(cellEnv(cell(container))).toEqual({});
    expect(cellEnv(cell())).toEqual({});
  });
});
