import { describe, expect, it } from "bun:test";
import { apiGateway, AWS, cloudflare, container, runsOn } from "./variants";

describe("the variants the catalogue offers", () => {
  it("runs every one of them on aws alone", () => {
    for (const one of AWS) {
      expect(runsOn(one, "aws")).toBe(true);
      expect(runsOn(one, "vps")).toBe(false);
      expect(runsOn(one, "dev")).toBe(false);
    }
  });

  it("alters the app's config, and nothing else", () => {
    expect(container.config).toEqual({ compute: "container" });
    expect(apiGateway.config).toEqual({ edge: "api-gateway" });
    expect(cloudflare.config).toEqual({ edge: "cloudflare" });
    for (const one of AWS) {
      expect(one.env).toBeUndefined();
    }
  });
});
