import { describe, expect, it } from "vitest";
import { source } from "./cli.js";
import { customLink } from "./custom.js";

describe("the record a custom link publishes as", () => {
  it("carries the properties verbatim, sourced to sst and granting nothing", () => {
    const record = customLink("network", {
      properties: {
        subnetIds: ["subnet-0a1", "subnet-0b2"],
        securityGroupIds: ["sg-0c3"],
        natEnabled: true,
        maxAzs: 3,
      },
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
    expect(() => customLink("", { properties: { vpcId: "vpc-1" } })).toThrow(
      /published under no name/,
    );
  });

  it("refuses a record with nothing to read", () => {
    expect(() => customLink("network", { properties: {} })).toThrow(
      /carries no properties/,
    );
  });

  it("refuses a property that never resolved", () => {
    expect(() =>
      customLink("network", { properties: { vpcId: undefined } }),
    ).toThrow(/vpcId as undefined/);
  });
});
