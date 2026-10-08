import { describe, expect, it } from "vitest";
import { defineConfig } from "../../config.js";
import { route53 } from "./dns.js";
import awsProvider from "./index.js";

describe("route53", () => {
  it("serialises without a zone when none is named", () => {
    expect(JSON.parse(JSON.stringify(route53()))).toEqual({ route53: {} });
  });

  it("type-checks as the aws provider's `dns` and serialises the zone it is given", () => {
    const config = defineConfig({
      slug: "test-app",
      provider: awsProvider({ dns: route53({ zone: "Z123456789ABCDEFGHIJK" }) }),
    });

    expect(JSON.parse(JSON.stringify(config.provider))).toEqual({
      aws: { dns: { route53: { zone: "Z123456789ABCDEFGHIJK" } } },
    });
  });
});
