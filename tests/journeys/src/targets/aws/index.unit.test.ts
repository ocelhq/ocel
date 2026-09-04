import { describe, expect, it } from "vitest";
import { offeredBy } from "../../offer";
import { awsTarget, bootstrapFeatures } from "./index";

describe("the features an aws bootstrap asks for", () => {
  it("names one edge feature for every edge the target offers, beside the next ones", () => {
    expect(bootstrapFeatures(offeredBy(awsTarget, {}))).toEqual([
      "isr",
      "image-optimization",
      "cloudfront-edge",
      "apigateway-edge",
      "cloudflare-edge",
    ]);
  });

  it("names only the edges a narrowed run enumerates", () => {
    expect(
      bootstrapFeatures(offeredBy(awsTarget, { OCEL_JOURNEY_EDGES: "cloudflare" })),
    ).toEqual(["isr", "image-optimization", "cloudflare-edge"]);
  });
});
