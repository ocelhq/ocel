import { defineConfig } from "ocel/config";
import { describe, expect, it } from "vitest";
import gcpProvider from "./index";

describe("gcpProvider", () => {
  it("returns a descriptor naming this package, carrying the project and region through", () => {
    expect(gcpProvider({ project: "acme-prod", region: "europe-west1" })).toEqual({
      package: "@ocel/provider-gcp",
      options: { project: "acme-prod", region: "europe-west1" },
    });
  });

  it("type-checks as an ocel.config.ts `provider` field and serializes to { package, options }", () => {
    const config = defineConfig({
      slug: "test-app",
      provider: gcpProvider({ project: "acme-prod", region: "europe-west1" }),
    });

    expect(JSON.parse(JSON.stringify(config.provider))).toEqual({
      package: "@ocel/provider-gcp",
      options: { project: "acme-prod", region: "europe-west1" },
    });
  });
});
