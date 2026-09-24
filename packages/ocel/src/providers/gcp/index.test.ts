import { describe, expect, it } from "vitest";
import { defineConfig } from "../../config.js";
import gcpProvider from "./index";

describe("gcpProvider", () => {
  it("returns its options keyed by the provider, carrying the project and region through", () => {
    expect(gcpProvider({ project: "acme-prod", region: "europe-west1" })).toEqual({
      gcp: { project: "acme-prod", region: "europe-west1" },
    });
  });

  it("type-checks as a `provider` field and serializes to its options keyed by gcp", () => {
    const config = defineConfig({
      slug: "test-app",
      provider: gcpProvider({ project: "acme-prod", region: "europe-west1" }),
    });

    expect(JSON.parse(JSON.stringify(config.provider))).toEqual({
      gcp: { project: "acme-prod", region: "europe-west1" },
    });
  });
});
