import { describe, it } from "bun:test";
import assert from "node:assert/strict";
import { cellBootstrapArgs } from "./bootstrap";

function features(args: string[]): string[] {
  return args[args.indexOf("--features") + 1].split(",");
}

describe("cellBootstrapArgs", () => {
  it("bootstraps the Cloudflare edge for a cloudflare cell", () => {
    assert.deepEqual(features(cellBootstrapArgs({ config: { edge: "cloudflare" } })), [
      "cloudflare-edge",
      "variables-key",
    ]);
  });

  it("names no edge feature for a CloudFront or API Gateway cell, whose first deploy adds its part", () => {
    for (const edge of [undefined, "cloudfront", "api-gateway"] as const) {
      assert.deepEqual(features(cellBootstrapArgs({ config: { edge } })), ["variables-key"]);
    }
  });

  it("refuses an edge no AWS bootstrap fronts", () => {
    assert.throws(() => cellBootstrapArgs({ config: { edge: "alb" } }), /alb edge/);
  });
});
