import { describe, expect, it } from "vitest";
import { source } from "./cli.js";
import { customBinding } from "./custom.js";

describe("the record a custom binding publishes as", () => {
  it("carries the properties verbatim, sourced to pulumi and granting nothing", () => {
    const record = customBinding("network", {
      subnetIds: ["subnet-0a1", "subnet-0b2"],
      securityGroupIds: ["sg-0c3"],
      natEnabled: true,
      maxAzs: 3,
    });

    expect(record).toEqual({
      name: "network",
      custom: {
        subnetIds: ["subnet-0a1", "subnet-0b2"],
        securityGroupIds: ["sg-0c3"],
        natEnabled: true,
        maxAzs: 3,
      },
      source,
    });
    expect(record).not.toHaveProperty("grants");
  });

  it("refuses a record under no name", () => {
    expect(() => customBinding("", { vpcId: "vpc-1" })).toThrow(/published under no name/);
  });

  it("refuses a record with nothing to read", () => {
    expect(() => customBinding("network", {})).toThrow(/carries no properties/);
  });

  it("refuses a property that never resolved", () => {
    expect(() => customBinding("network", { vpcId: undefined })).toThrow(/vpcId as undefined/);
  });
});
