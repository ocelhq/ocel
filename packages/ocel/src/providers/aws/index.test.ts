import { describe, expect, it } from "vitest";
import { defineConfig } from "../../config.js";
import awsProvider from "./index";

describe("awsProvider", () => {
  it("returns a descriptor naming the provider, carrying the given options", () => {
    expect(awsProvider({ region: "us-east-1" })).toEqual({
      name: "aws",
      options: { region: "us-east-1" },
    });
  });

  it("carries the ordered transform module list through to the provider", () => {
    const config = defineConfig({
      slug: "test-app",
      provider: awsProvider({
        transforms: ["./infra/defaults.transform.ts", "./infra/vpc.transform.ts"],
      }),
    });

    expect(JSON.parse(JSON.stringify(config.provider))).toEqual({
      name: "aws",
      options: {
        transforms: ["./infra/defaults.transform.ts", "./infra/vpc.transform.ts"],
      },
    });
  });

  it("leaves the options bag without a transforms key when none is authored", () => {
    expect(
      Object.hasOwn(awsProvider({ region: "us-east-1" }).options as object, "transforms"),
    ).toBe(false);
  });

  it("carries already-issued certificate arns through, keyed by hostname", () => {
    expect(
      awsProvider({
        certificates: {
          "app.acme.com": "arn:aws:acm:us-east-1:111122223333:certificate/abcd-1234",
        },
      }),
    ).toEqual({
      name: "aws",
      options: {
        certificates: {
          "app.acme.com": "arn:aws:acm:us-east-1:111122223333:certificate/abcd-1234",
        },
      },
    });
  });

  it("carries the arn of a key the account brought through to the provider", () => {
    expect(awsProvider({ varsKey: "arn:aws:kms:eu-west-1:111122223333:key/abcd-1234" })).toEqual({
      name: "aws",
      options: { varsKey: "arn:aws:kms:eu-west-1:111122223333:key/abcd-1234" },
    });
  });

  it("defaults options to an empty object when called with none", () => {
    expect(awsProvider()).toEqual({
      name: "aws",
      options: {},
    });
  });

  it("type-checks as a `provider` field and serializes to { name, options }", () => {
    const config = defineConfig({
      slug: "test-app",
      provider: awsProvider({ region: "us-east-1" }),
    });

    expect(JSON.parse(JSON.stringify(config.provider))).toEqual({
      name: "aws",
      options: { region: "us-east-1" },
    });
  });
});
