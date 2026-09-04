import { describe, expect, it } from "vitest";
import { failureFor } from "./prepare";

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
