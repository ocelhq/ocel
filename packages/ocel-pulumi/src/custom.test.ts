import { describe, expect, it } from "vitest";
import { source } from "./cli.js";
import { publishedCustom } from "./custom.js";

describe("the record a custom binding publishes as", () => {
  it("passes the properties through verbatim, sourced to pulumi and granting nothing", () => {
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

  it("refuses a record with nothing to read and nothing to grant", () => {
    expect(() => publishedCustom("network", {})).toThrow(/has no properties and no grants/);
  });

  it("publishes the grants beside a record with no properties", () => {
    const grants = [
      { role: "roles/compute.instanceAdmin.v1", resource: "projects/ocel-cloud-prod" },
    ];

    expect(publishedCustom("fleet", {}, grants)).toEqual({
      binding: { name: "fleet", custom: {}, source },
      grants,
    });
  });

  it("refuses a grant over every resource", () => {
    expect(() =>
      publishedCustom("fleet", {}, [{ role: "roles/compute.instanceAdmin.v1", resource: "*" }]),
    ).toThrow(/resource it binds and nothing else/);
  });

  it("refuses a property that never resolved", () => {
    expect(() => publishedCustom("network", { vpcId: undefined })).toThrow(/vpcId as undefined/);
  });
});
