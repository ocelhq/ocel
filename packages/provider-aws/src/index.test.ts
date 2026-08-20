import { defineConfig } from "ocel/config";
import { describe, expect, it } from "vitest";
import awsProvider from "./index";

describe("awsProvider", () => {
  it("returns a descriptor naming this package, carrying the given options", () => {
    expect(awsProvider({ region: "us-east-1" })).toEqual({
      package: "@ocel/provider-aws",
      options: { aws: { region: "us-east-1" } },
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
      package: "@ocel/provider-aws",
      options: {
        aws: {
          transforms: [
            "./infra/defaults.transform.ts",
            "./infra/vpc.transform.ts",
          ],
        },
      },
    });
  });

  it("leaves the options bag without a transforms key when none is authored", () => {
    expect(
      Object.hasOwn(
        (awsProvider({ region: "us-east-1" }).options as { aws: object }).aws,
        "transforms",
      ),
    ).toBe(false);
  });

  it("carries already-issued certificate arns through, keyed by hostname", () => {
    expect(
      awsProvider({
        certificates: {
          "app.acme.com":
            "arn:aws:acm:us-east-1:111122223333:certificate/abcd-1234",
        },
      }),
    ).toEqual({
      package: "@ocel/provider-aws",
      options: {
        aws: {
          certificates: {
            "app.acme.com":
              "arn:aws:acm:us-east-1:111122223333:certificate/abcd-1234",
          },
        },
      },
    });
  });

  it("defaults options to an empty object when called with none", () => {
    expect(awsProvider()).toEqual({
      package: "@ocel/provider-aws",
      options: { aws: {} },
    });
  });

  it("type-checks as an ocel.config.ts `provider` field and serializes to { package, options }", () => {
    const config = defineConfig({
      slug: "test-app",
      provider: awsProvider({ region: "us-east-1" }),
    });

    expect(JSON.parse(JSON.stringify(config.provider))).toEqual({
      package: "@ocel/provider-aws",
      options: { aws: { region: "us-east-1" } },
    });
  });
});
