import contract from "@framework/next-cache/fixtures/edge-contract.json" with { type: "json" };
import { describe, expect, it } from "vitest";

import { OCEL_REVALIDATED } from "../src/index.mjs";

describe("the origin's revalidation announcement", () => {
  it("is read under the header name the origin writes", () => {
    expect(OCEL_REVALIDATED).toBe(contract.revalidatedHeader);
  });
});
