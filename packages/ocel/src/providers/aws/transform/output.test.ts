import { describe, expect, it } from "vitest";
import { defineTransform } from "./define";
import { type EvaluateRequest, evaluate, type TransformModule } from "./evaluate";
import { bindings, isBindingOutput, outputPlaceholderKey } from "./output";

function request(): EvaluateRequest {
  return {
    envClass: "production",
    env: "prod",
    resources: [
      {
        type: "function",
        name: "api-users",
        app: "api",
        surfaces: {
          lambda: {
            memorySizeMb: 1024,
            timeoutSeconds: 30,
            runtime: "nodejs24.x",
          },
          url: { invokeMode: "RESPONSE_STREAM" },
          vpc: { subnetIds: [], securityGroupIds: [] },
        },
      },
    ],
  };
}

function transformModule(
  specifier: string,
  rules: ReturnType<typeof defineTransform>,
): TransformModule {
  return { specifier, rules };
}

describe("bindings", () => {
  it("names one property of one published record", () => {
    expect(bindings.network!.lambdaSecurityGroupId).toEqual({
      [outputPlaceholderKey]: {
        binding: "network",
        property: "lambdaSecurityGroupId",
      },
    });
  });

  it("hands out a frozen placeholder that names no list", () => {
    const placeholder = bindings.network!.privateSubnetIds!;

    expect(Object.isFrozen(placeholder)).toBe(true);
    expect(Object.isFrozen(placeholder[outputPlaceholderKey])).toBe(true);
    expect(Object.keys(placeholder[outputPlaceholderKey])).toEqual(["binding", "property"]);
  });

  it("refuses an output that names no binding or no property", () => {
    expect(() => bindings[""]!.privateSubnetIds).toThrow(/names no binding/);
    expect(() => bindings.network![""]).toThrow(/names no property/);
  });

  it("manufactures nothing for a symbol or for the thenable trap", async () => {
    const published = bindings as Record<string | symbol, unknown>;
    const network = bindings.network as Record<string | symbol, unknown>;

    expect(published.then).toBeUndefined();
    expect(published[Symbol.iterator]).toBeUndefined();
    expect(network.then).toBeUndefined();
    expect(network[Symbol.toPrimitive]).toBeUndefined();
    expect(() => `${network}`).toThrow(TypeError);
    await expect(Promise.resolve(network)).resolves.toBe(network);
  });

  it("recognises what it authored and nothing else", () => {
    expect(isBindingOutput(bindings.network!.vpcId)).toBe(true);
    expect(isBindingOutput("subnet-a")).toBe(false);
    expect(isBindingOutput(null)).toBe(false);
    expect(isBindingOutput(["subnet-a"])).toBe(false);
  });
});

describe("evaluate with binding outputs", () => {
  it("carries a placeholder through to the deploy in place of a value", () => {
    const got = evaluate(request(), [
      transformModule(
        "vpc.ts",
        defineTransform(({ bindings: published }) => ({
          function: {
            vpc: {
              subnetIds: published.network!.privateSubnetIds,
              securityGroupIds: published.network!.lambdaSecurityGroupIds,
            },
          },
        })),
      ),
    ]);

    expect(got.resources[0]!.surfaces.vpc).toEqual({
      subnetIds: bindings.network!.privateSubnetIds,
      securityGroupIds: bindings.network!.lambdaSecurityGroupIds,
    });
  });

  it("serializes to what the deploy decodes", () => {
    expect(JSON.parse(JSON.stringify(bindings.network!.privateSubnetIds))).toEqual({
      $ocelOutput: { binding: "network", property: "privateSubnetIds" },
    });
  });

  it("holds an override to the same allowlist when it fills a field with an output", () => {
    expect(() =>
      evaluate(request(), [
        transformModule(
          "vpc.ts",
          defineTransform({
            function: {
              vpc: () => ({ subnetIds: bindings.network!.privateSubnetIds }) as never,
            },
          }),
        ),
      ]),
    ).toThrow(/without securityGroupIds/);
  });

  it("refuses an output on a field this provider does not expose", () => {
    expect(() =>
      evaluate(request(), [
        transformModule(
          "vpc.ts",
          defineTransform({
            function: {
              vpc: { vpcId: bindings.network!.vpcId } as never,
            },
          }),
        ),
      ]),
    ).toThrow(/function\.vpc\.vpcId/);
  });
});
