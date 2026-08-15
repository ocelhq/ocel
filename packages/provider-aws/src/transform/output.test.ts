import { describe, expect, it } from "vitest";
import { defineTransform } from "./define";
import { evaluate, type EvaluateRequest, type TransformModule } from "./evaluate";
import {
  isLinkOutput,
  output,
  outputList,
  outputPlaceholderKey,
} from "./output";

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

describe("output", () => {
  it("names one property of one published record", () => {
    expect(output("network", "lambdaSecurityGroupId")).toEqual({
      [outputPlaceholderKey]: {
        link: "network",
        property: "lambdaSecurityGroupId",
      },
    });
  });

  it("marks a list output so the deploy splits the published value", () => {
    expect(outputList("network", "privateSubnetIds")).toEqual({
      [outputPlaceholderKey]: {
        link: "network",
        property: "privateSubnetIds",
        list: true,
      },
    });
  });

  it("refuses an output that names no link or no property", () => {
    expect(() => output("", "privateSubnetIds")).toThrow(/names no link/);
    expect(() => outputList("network", "")).toThrow(/names no property/);
  });

  it("recognises what it authored and nothing else", () => {
    expect(isLinkOutput(output("network", "vpcId"))).toBe(true);
    expect(isLinkOutput("subnet-a")).toBe(false);
    expect(isLinkOutput(null)).toBe(false);
    expect(isLinkOutput(["subnet-a"])).toBe(false);
  });
});

describe("evaluate with link outputs", () => {
  it("carries a placeholder through to the deploy in place of a value", () => {
    const got = evaluate(request(), [
      transformModule(
        "vpc.ts",
        defineTransform({
          function: {
            vpc: {
              subnetIds: outputList("network", "privateSubnetIds"),
              securityGroupIds: [output("network", "lambdaSecurityGroupId")],
            },
          },
        }),
      ),
    ]);

    expect(got.resources[0]!.surfaces.vpc).toEqual({
      subnetIds: outputList("network", "privateSubnetIds"),
      securityGroupIds: [output("network", "lambdaSecurityGroupId")],
    });
  });

  it("serializes to what the deploy decodes", () => {
    expect(JSON.parse(JSON.stringify(outputList("network", "privateSubnetIds")))).toEqual({
      $ocelOutput: {
        link: "network",
        property: "privateSubnetIds",
        list: true,
      },
    });
  });

  it("holds an override to the same allowlist when it fills a field with an output", () => {
    expect(() =>
      evaluate(request(), [
        transformModule(
          "vpc.ts",
          defineTransform({
            function: {
              vpc: () =>
                ({ subnetIds: output("network", "privateSubnetIds") }) as never,
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
              vpc: { vpcId: output("network", "vpcId") } as never,
            },
          }),
        ),
      ]),
    ).toThrow(/function\.vpc\.vpcId/);
  });
});
