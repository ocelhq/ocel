import { describe, expect, it } from "vitest";
import { defineTransform } from "../define.js";
import { type EvaluateRequest, evaluate, type TransformModule } from "../evaluate.js";

function request(): EvaluateRequest {
  return {
    provider: "gcp",
    envTier: "production",
    env: "production",
    resources: [{ type: "bucket", name: "uploads" }],
  };
}

function module(
  specifier: string,
  definition: ReturnType<typeof defineTransform>,
): TransformModule {
  return { specifier, definition };
}

describe("the gcp branch", () => {
  it("hands the provider the paths a rule set, keyed by the resource it constructs", () => {
    const modules = [
      module(
        "./storage.transform.ts",
        defineTransform({
          gcp: {
            bucket: {
              bucket: {
                storageClass: "NEARLINE",
                versioning: { enabled: true },
                lifecycleRules: [{ action: { type: "Delete" }, condition: { age: 30 } }],
              },
            },
          },
        }),
      ),
    ];

    expect(evaluate(request(), modules).resources[0]?.patches).toEqual({
      bucket: {
        storageClass: "NEARLINE",
        versioning: { enabled: true },
        lifecycleRules: [{ action: { type: "Delete" }, condition: { age: 30 } }],
      },
    });
  });

  it("refuses a path the provider has no translation for, by name", () => {
    const modules = [
      module(
        "./owned.transform.ts",
        defineTransform({
          gcp: {
            bucket: {
              bucket: { lifecycleRules: [{ condition: { sizeAboveBytes: 1 } }] } as never,
            },
          },
        }),
      ),
    ];

    expect(() => evaluate(request(), modules)).toThrow(
      /gcp\.bucket\.bucket\.lifecycleRules\.condition\.sizeAboveBytes/,
    );
  });

  it.each([
    [{ cors: [] }, "cors"],
    [{ cors: null }, "cors"],
    [{ autoclass: {} }, "autoclass"],
  ])(
    "refuses %o, which sets a path the provider has no translation for to nothing",
    (patch, named) => {
      const modules = [
        module(
          "./empty.transform.ts",
          defineTransform({ gcp: { bucket: { bucket: patch as never } } }),
        ),
      ];

      expect(() => evaluate(request(), modules)).toThrow(
        new RegExp(`a rule sets gcp\\.bucket\\.bucket\\.${named},`),
      );
    },
  );

  it("refuses a field ocel sets itself, by name", () => {
    const modules = [
      module(
        "./owned.transform.ts",
        defineTransform({ gcp: { bucket: { bucket: { location: "US" } as never } } }),
      ),
    ];

    expect(() => evaluate(request(), modules)).toThrow(/gcp\.bucket\.bucket\.location/);
  });
});
