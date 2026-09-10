import { describe, expect, it } from "vitest";
import { defineTransform } from "./define";
import { type EvaluateRequest, evaluate, type TransformModule } from "./evaluate";

function request(overrides: Partial<EvaluateRequest> = {}): EvaluateRequest {
  return {
    provider: "aws",
    envClass: "production",
    env: "production",
    resources: [{ type: "function", name: "api", app: "web" }],
    ...overrides,
  };
}

function module(
  specifier: string,
  definition: ReturnType<typeof defineTransform>,
): TransformModule {
  return { specifier, definition };
}

describe("evaluate", () => {
  it("hands the provider the patch a rule wrote, keyed by the resource it constructs", () => {
    const modules = [
      module(
        "./tuning.transform.ts",
        defineTransform({ aws: { function: { lambda: { memorySize: 1024 } } } }),
      ),
    ];

    expect(evaluate(request(), modules).resources[0]).toEqual({
      name: "api",
      patches: { lambda: { memorySize: 1024 } },
      tags: {},
    });
  });

  it("fills a leaf from the record a custom binding published", () => {
    const modules = [
      module(
        "./network.transform.ts",
        defineTransform(({ bindings }) => ({
          aws: {
            function: {
              lambda: { vpcConfig: { subnetIds: bindings.custom!.network!.subnetIds! } },
            },
          },
        })),
      ),
    ];

    expect(evaluate(request(), modules).resources[0]?.patches.lambda).toEqual({
      vpcConfig: {
        subnetIds: { $ocelOutput: { type: "custom", name: "network", property: "subnetIds" } },
      },
    });
  });

  it("refuses a field ocel fills from what the deploy built, by name", () => {
    const modules = [
      module(
        "./role.transform.ts",
        defineTransform({
          aws: { function: { lambda: { role: "arn:aws:iam::1:role/mine" } } },
        } as never),
      ),
    ];

    expect(() => evaluate(request(), modules)).toThrow(/aws\.function\.lambda\.role/);
  });

  it.each([
    ["runtime", "python3.13"],
    ["code", "./mine.zip"],
    ["imageUri", "1.dkr.ecr.us-east-1.amazonaws.com/mine:latest"],
    ["packageType", "Image"],
    ["s3ObjectVersion", "an-older-object"],
    ["sourceCodeHash", "deadbeef"],
    ["architectures", ["arm64"]],
    ["name", "mine"],
    ["tags", { team: "core" }],
  ])("refuses %s, which would unpair the lambda from the artifact ocel built", (field, value) => {
    const modules = [
      module(
        "./code.transform.ts",
        defineTransform({ aws: { function: { lambda: { [field]: value } } } } as never),
      ),
    ];

    expect(() => evaluate(request(), modules)).toThrow(
      new RegExp(`aws\\.function\\.lambda\\.${field}`),
    );
  });

  it.each(["runtime", "code", "imageUri", "packageType", "s3ObjectVersion", "architectures"])(
    "refuses %s on the upload completer, whose code ocel places itself",
    (field) => {
      const modules = [
        module(
          "./completer.transform.ts",
          defineTransform({
            aws: { bucket: { uploadCompleter: { [field]: "whatever" } } },
          } as never),
        ),
      ];

      expect(() =>
        evaluate(request({ resources: [{ type: "bucket", name: "uploads" }] }), modules),
      ).toThrow(new RegExp(`aws\\.bucket\\.uploadCompleter\\.${field}`));
    },
  );

  it("carries a patch for every resource the aws provider constructs", () => {
    const modules = [
      module(
        "./everything.transform.ts",
        defineTransform({
          aws: { function: { role: { path: "/ocel/" }, urlPermission: { statementId: "open" } } },
        }),
      ),
    ];

    expect(evaluate(request(), modules).resources[0]?.patches).toEqual({
      role: { path: "/ocel/" },
      urlPermission: { statementId: "open" },
    });
  });

  it("refuses a module with no branch for the provider this project deploys to", () => {
    const modules = [module("./gcp.transform.ts", defineTransform({ gcp: {} } as never))];

    expect(() => evaluate(request(), modules)).toThrow(
      /\.\/gcp\.transform\.ts patches gcp and this project deploys to aws/,
    );
  });

  it("lets the later module win where two patch the same field", () => {
    const modules = [
      module(
        "./a.transform.ts",
        defineTransform({ aws: { function: { lambda: { memorySize: 512 } } } }),
      ),
      module(
        "./b.transform.ts",
        defineTransform({ aws: { function: { lambda: { memorySize: 1024, timeout: 60 } } } }),
      ),
    ];

    expect(evaluate(request(), modules).resources[0]?.patches.lambda).toEqual({
      memorySize: 1024,
      timeout: 60,
    });
  });

  it("merges nested patches rather than replacing them", () => {
    const modules = [
      module(
        "./a.transform.ts",
        defineTransform({ aws: { function: { lambda: { vpcConfig: { subnetIds: ["a"] } } } } }),
      ),
      module(
        "./b.transform.ts",
        defineTransform({
          aws: { function: { lambda: { vpcConfig: { securityGroupIds: ["b"] } } } },
        }),
      ),
    ];

    expect(evaluate(request(), modules).resources[0]?.patches.lambda).toEqual({
      vpcConfig: { subnetIds: ["a"], securityGroupIds: ["b"] },
    });
  });

  it("skips a rule whose gate reads false for the resource", () => {
    const modules = [
      module(
        "./preview.transform.ts",
        defineTransform({
          if: (ctx) => ctx.envClass === "preview",
          aws: { function: { lambda: { memorySize: 128 } } },
        }),
      ),
    ];

    expect(evaluate(request(), modules).resources[0]?.patches).toEqual({});
  });

  it("hands the callback the environment being deployed", () => {
    let seen: string[] = [];
    const modules = [
      module(
        "./env.transform.ts",
        defineTransform((inputs) => {
          seen = [inputs.envClass, inputs.env];
          return { aws: {} };
        }),
      ),
    ];

    evaluate(request({ envClass: "preview", env: "pr-12" }), modules);

    expect(seen).toEqual(["preview", "pr-12"]);
  });

  it("unions the tags a rule carries and refuses ocel's own prefix", () => {
    const kept = [
      module("./tags.transform.ts", defineTransform({ tags: { team: "core" }, aws: {} })),
    ];
    expect(evaluate(request(), kept).resources[0]?.tags).toEqual({ team: "core" });

    const reserved = [
      module("./tags.transform.ts", defineTransform({ tags: { "ocel:app": "web" }, aws: {} })),
    ];
    expect(() => evaluate(request(), reserved)).toThrow(/ocel:/);
  });

  it("refuses a patch on a resource this provider does not construct", () => {
    const modules = [
      module("./bad.transform.ts", defineTransform({ aws: { function: { queue: {} } } } as never)),
    ];

    expect(() => evaluate(request(), modules)).toThrow(/aws\.function\.queue/);
  });

  it("refuses a rule targeting a resource type this provider does not render", () => {
    const modules = [
      module("./bad.transform.ts", defineTransform({ aws: { queue: { queue: {} } } } as never)),
    ];

    expect(() => evaluate(request(), modules)).toThrow(/aws\.queue/);
  });
});
