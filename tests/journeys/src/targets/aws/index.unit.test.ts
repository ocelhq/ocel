import { describe, expect, it } from "bun:test";
import { bootstrapFeatures } from "./index";

describe("the features the one aws bootstrap applies", () => {
  it("asks a real account for every feature the provider offers", () => {
    expect(bootstrapFeatures("real")).toBe("all");
  });

  it("leaves the cloudflare edge out under floci, where nothing answers for the Cloudflare API", () => {
    expect(bootstrapFeatures("floci")).toBe("isr,image-optimization,cloudfront-edge,apigateway-edge");
  });
});
