import { describe, expect, it } from "vitest";
import { apiGateway, cloudfront } from "./edge.js";

describe("cloudfront", () => {
  it("keys its options by the cloudfront edge", () => {
    expect(cloudfront()).toEqual({ cloudfront: {} });
  });

  it("passes the origin shield option through", () => {
    expect(cloudfront({ originShield: false })).toEqual({ cloudfront: { originShield: false } });
  });
});

describe("apiGateway", () => {
  it("keys its options by the api-gateway edge", () => {
    expect(apiGateway()).toEqual({ "api-gateway": {} });
  });
});
