import contract from "@framework/next-cache/fixtures/edge-contract.json" with { type: "json" };
import { purgeHeader } from "@platform/edge-contract/purge";
import { describe, expect, it } from "vitest";

describe("the origin's purge announcement", () => {
  it("is read under the header name the origin writes", () => {
    expect(purgeHeader).toBe(contract.purgeHeader);
  });
});
