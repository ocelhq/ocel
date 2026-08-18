import { defineConfig } from "ocel/config";
import { describe, expect, it } from "vitest";
import { route53 } from "./dns";

describe("route53", () => {
  it("serialises without a zone when none is named", () => {
    expect(JSON.parse(JSON.stringify(route53()))).toEqual({ kind: "route53" });
  });

  it("type-checks as an ocel.config.ts `dns` field and serialises the zone it is given", () => {
    const config = defineConfig({
      slug: "test-app",
      dns: route53({ zone: "Z123456789ABCDEFGHIJK" }),
    });

    expect(JSON.parse(JSON.stringify(config.dns))).toEqual({
      kind: "route53",
      zone: "Z123456789ABCDEFGHIJK",
    });
  });
});
