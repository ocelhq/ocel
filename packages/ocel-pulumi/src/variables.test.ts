import { describe, expect, it } from "vitest";
import { publishedValues } from "./variables.js";

describe("the values bind.variables publishes as", () => {
  it("keeps text verbatim, writes anything else as JSON, and marks only what was secret", () => {
    expect(
      publishedValues(
        {
          DATABASE_URL: "postgres://db.internal:5432/app",
          API_KEY: "k3y",
          MAX_CONNECTIONS: 20,
          FEATURE_FLAGS: { search: true },
          REGIONS: ["eu-west-1", "us-east-1"],
          CACHE_ENABLED: false,
        },
        ["API_KEY"],
      ),
    ).toEqual({
      values: [
        { key: "API_KEY", value: "k3y", secret: true },
        { key: "CACHE_ENABLED", value: "false" },
        { key: "DATABASE_URL", value: "postgres://db.internal:5432/app" },
        { key: "FEATURE_FLAGS", value: '{"search":true}' },
        { key: "MAX_CONNECTIONS", value: "20" },
        { key: "REGIONS", value: '["eu-west-1","us-east-1"]' },
      ],
    });
  });

  it("refuses a call with nothing to publish", () => {
    expect(() => publishedValues({}, [])).toThrow(/publishes no values/);
  });

  it("refuses a value under no name", () => {
    expect(() => publishedValues({ "": "x" }, [])).toThrow(/no name/);
  });

  it("refuses a value that never resolved", () => {
    expect(() => publishedValues({ API_KEY: undefined }, [])).toThrow(/API_KEY has no value/);
    expect(() => publishedValues({ API_KEY: null }, [])).toThrow(/API_KEY has no value/);
  });
});
