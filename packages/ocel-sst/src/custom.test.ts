import { describe, expect, it } from "vitest";
import { source } from "./cli.js";
import { publishedCustom } from "./custom.js";

describe("the record a custom binding publishes as", () => {
  it("passes the properties through verbatim, sourced to sst and granting nothing", () => {
    const record = publishedCustom("network", {
      subnetIds: ["subnet-0a1", "subnet-0b2"],
      securityGroupIds: ["sg-0c3"],
      natEnabled: true,
      maxAzs: 3,
    });

    expect(record).toEqual({
      binding: {
        name: "network",
        custom: {
          subnetIds: ["subnet-0a1", "subnet-0b2"],
          securityGroupIds: ["sg-0c3"],
          natEnabled: true,
          maxAzs: 3,
        },
        source,
      },
    });
    expect(record).not.toHaveProperty("grants");
  });

  it("refuses a record under no name", () => {
    expect(() => publishedCustom("", { vpcId: "vpc-1" })).toThrow(/published under no name/);
  });

  it("refuses a record with nothing to read", () => {
    expect(() => publishedCustom("network", {})).toThrow(/has no properties/);
  });

  it("refuses a property that never resolved", () => {
    expect(() => publishedCustom("network", { vpcId: undefined })).toThrow(/vpcId as undefined/);
  });
});
