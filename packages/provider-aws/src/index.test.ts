import { defineConfig } from "@ocel/sdk/config";
import { describe, expect, it } from "vitest";
import awsProvider from "./index";

describe("awsProvider", () => {
  it("returns a descriptor naming this package, carrying the given options", () => {
    expect(awsProvider({ region: "us-east-1" })).toEqual({
      package: "@ocel/provider-aws",
      options: { region: "us-east-1" },
    });
  });

  it("defaults options to an empty object when called with none", () => {
    expect(awsProvider()).toEqual({
      package: "@ocel/provider-aws",
      options: {},
    });
  });

  it("type-checks as an ocel.config.ts `provider` field and serializes to { package, options }", () => {
    const config = defineConfig({
      projectId: "proj_123",
      provider: awsProvider({ region: "us-east-1" }),
    });

    expect(JSON.parse(JSON.stringify(config.provider))).toEqual({
      package: "@ocel/provider-aws",
      options: { region: "us-east-1" },
    });
  });
});
