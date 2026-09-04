import { describe, expect, it } from "vitest";
import { offeredBy } from "../../offer";
import { applyFeatures, awsTarget, dryPlans } from "./index";

describe("the plans an aws lane dries out before it bootstraps", () => {
  it("asks each offered edge for its own feature", () => {
    expect(dryPlans(offeredBy(awsTarget, {}))).toEqual([
      { edge: "cloudfront", feature: "cloudfront-edge" },
      { edge: "api-gateway", feature: "apigateway-edge" },
      { edge: "cloudflare", feature: "cloudflare-edge" },
    ]);
  });

  it("names only the edges a narrowed run enumerates", () => {
    expect(dryPlans(offeredBy(awsTarget, { OCEL_JOURNEY_EDGES: "cloudflare" }))).toEqual([
      { edge: "cloudflare", feature: "cloudflare-edge" },
    ]);
  });
});

describe("the features the one real aws bootstrap applies", () => {
  it("carries the next features and every edge that planned", () => {
    const offered = offeredBy(awsTarget, {});
    expect(applyFeatures(offered, offered.edges)).toEqual([
      "isr",
      "image-optimization",
      "cloudfront-edge",
      "apigateway-edge",
      "cloudflare-edge",
    ]);
  });

  it("leaves out an edge whose dry plan failed", () => {
    expect(applyFeatures(offeredBy(awsTarget, {}), ["cloudfront", "api-gateway"])).toEqual([
      "isr",
      "image-optimization",
      "cloudfront-edge",
      "apigateway-edge",
    ]);
  });

  it("asks for one edge alone when the run is narrowed to it", () => {
    const offered = offeredBy(awsTarget, { OCEL_JOURNEY_EDGES: "cloudflare" });
    expect(applyFeatures(offered, offered.edges)).toEqual([
      "isr",
      "image-optimization",
      "cloudflare-edge",
    ]);
  });

  it("asks for the next features alone when no edge planned", () => {
    expect(applyFeatures(offeredBy(awsTarget, {}), [])).toEqual(["isr", "image-optimization"]);
  });
});
