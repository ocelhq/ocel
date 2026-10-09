import { describe, it } from "bun:test";
import assert from "node:assert/strict";
import { cellBootstrapArgs } from "./bootstrap";

function features(args: string[]): string[] {
  return args[args.indexOf("--features") + 1].split(",");
}

describe("cellBootstrapArgs", () => {
  it("bootstraps a cloudflare cell without the CloudFront or API Gateway fronts", () => {
    const named = features(cellBootstrapArgs({ config: { edge: "cloudflare" } }));
    assert.ok(named.includes("cloudflare-edge"));
    assert.ok(!named.includes("cloudfront-edge"));
    assert.ok(!named.includes("apigateway-edge"));
  });

  it("bootstraps an api gateway cell without a CloudFront cache policy or a Cloudflare edge", () => {
    const named = features(cellBootstrapArgs({ config: { edge: "api-gateway" } }));
    assert.ok(named.includes("apigateway-edge"));
    assert.ok(!named.includes("cloudfront-edge"));
    assert.ok(!named.includes("cloudflare-edge"));
  });

  it("bootstraps CloudFront for a cell that names no edge, the one AWS defaults to", () => {
    const named = features(cellBootstrapArgs({ config: {} }));
    assert.ok(named.includes("cloudfront-edge"));
    assert.ok(!named.includes("apigateway-edge"));
    assert.ok(!named.includes("cloudflare-edge"));
  });

  it("keeps the variables key every cell's env set writes under", () => {
    for (const edge of [undefined, "cloudflare", "api-gateway"] as const) {
      assert.ok(features(cellBootstrapArgs({ config: { edge } })).includes("variables-key"));
    }
  });
});
