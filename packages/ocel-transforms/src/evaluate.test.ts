import { describe, expect, it } from "vitest";
import { defineTransform } from "./define.js";
import { type EvaluateRequest, evaluate, type TransformModule } from "./evaluate.js";

function request(overrides: Partial<EvaluateRequest> = {}): EvaluateRequest {
  return {
    provider: "aws",
    envTier: "production",
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

  it("patches a container app's task, service, target group, rule and role", () => {
    const modules = [
      module(
        "./container.transform.ts",
        defineTransform({
          aws: {
            container: {
              task: { cpu: "1024", memory: "2048" },
              service: { healthCheckGracePeriodSeconds: 120 },
              targetGroup: { deregistrationDelay: 10 },
              rule: { transforms: [] },
              role: { description: "the web container" },
            },
          },
        }),
      ),
    ];

    expect(
      evaluate(request({ resources: [{ type: "container", name: "web", app: "web" }] }), modules)
        .resources[0]?.patches,
    ).toEqual({
      task: { cpu: "1024", memory: "2048" },
      service: { healthCheckGracePeriodSeconds: 120 },
      targetGroup: { deregistrationDelay: 10 },
      rule: { transforms: [] },
      role: { description: "the web container" },
    });
  });

  it.each(["function", "container"])(
    "hands the provider the managed policies a rule attaches to a %s's role",
    (type) => {
      const modules = [
        module(
          "./grants.transform.ts",
          defineTransform(({ bindings }) => ({
            aws: {
              [type]: {
                role: {
                  managedPolicyArns: [
                    "arn:aws:iam::123456789012:policy/reports-read",
                    bindings.custom!.grants!.policyArn!,
                  ],
                },
              },
            },
          })),
        ),
      ];

      expect(
        evaluate(request({ resources: [{ type, name: "web", app: "web" }] }), modules).resources[0]
          ?.patches,
      ).toEqual({
        role: {
          managedPolicyArns: [
            "arn:aws:iam::123456789012:policy/reports-read",
            { $ocelOutput: { type: "custom", name: "grants", property: "policyArn" } },
          ],
        },
      });
    },
  );

  it.each([
    ["task", "containerDefinitions"],
    ["task", "family"],
    ["task", "runtimePlatform"],
    ["task", "taskRoleArn"],
    ["task", "executionRoleArn"],
    ["service", "taskDefinition"],
    ["service", "cluster"],
    ["service", "networkConfiguration"],
    ["service", "loadBalancers"],
    ["service", "desiredCount"],
    ["targetGroup", "vpcId"],
    ["targetGroup", "port"],
    ["rule", "conditions"],
    ["rule", "priority"],
    ["role", "assumeRolePolicy"],
    ["role", "permissionsBoundary"],
  ])("refuses %s.%s on a container app, which ocel fills from what it built", (key, field) => {
    const modules = [
      module(
        "./container.transform.ts",
        defineTransform({ aws: { container: { [key]: { [field]: "whatever" } } } } as never),
      ),
    ];

    expect(() =>
      evaluate(request({ resources: [{ type: "container", name: "web", app: "web" }] }), modules),
    ).toThrow(new RegExp(`aws\\.container\\.${key}\\.${field}`));
  });

  it("drops a kv store to one node", () => {
    const modules = [
      module(
        "./single.transform.ts",
        defineTransform({
          aws: {
            kv: {
              replicationGroup: {
                numCacheClusters: 1,
                automaticFailoverEnabled: false,
                multiAzEnabled: false,
              },
            },
          },
        }),
      ),
    ];

    expect(
      evaluate(request({ resources: [{ type: "kv", name: "cache" }] }), modules).resources[0]
        ?.patches,
    ).toEqual({
      replicationGroup: {
        numCacheClusters: 1,
        automaticFailoverEnabled: false,
        multiAzEnabled: false,
      },
    });
  });

  it.each([
    "authToken",
    "transitEncryptionEnabled",
    "port",
    "engineVersion",
    "userGroupIds",
    "nodeType",
  ])(
    "refuses %s on a kv store's replication group, which ocel fills from the store's declaration",
    (field) => {
      const modules = [
        module(
          "./kv.transform.ts",
          defineTransform({ aws: { kv: { replicationGroup: { [field]: "whatever" } } } } as never),
        ),
      ];

      expect(() =>
        evaluate(request({ resources: [{ type: "kv", name: "cache" }] }), modules),
      ).toThrow(new RegExp(`aws\\.kv\\.replicationGroup\\.${field}`));
    },
  );

  it.each([
    ["privateSubnet", "cidrBlock"],
    ["publicSubnet", "mapPublicIpOnLaunch"],
    ["isolatedRouteTable", "routes"],
    ["s3Endpoint", "routeTableIds"],
    ["natGateway", "subnetId"],
    ["vpc", "enableDnsSupport"],
    ["vpc", "enableDnsHostnames"],
    ["vpc", "instanceTenancy"],
    ["isolatedSubnet", "ipv6Native"],
    ["natGateway", "connectivityType"],
    ["privateSubnet", "ipv6CidrBlock"],
    ["publicSubnet", "outpostArn"],
    ["natGateway", "privateIp"],
    ["natGateway", "secondaryAllocationIds"],
    ["natGateway", "secondaryPrivateIpAddresses"],
    ["natAddress", "address"],
    ["natAddress", "instance"],
    ["natAddress", "networkInterface"],
    ["natAddress", "associateWithPrivateIp"],
  ])("refuses %s.%s on the tier's network, whose layout ocel keeps", (key, field) => {
    const modules = [
      module(
        "./network.transform.ts",
        defineTransform({ aws: { network: { [key]: { [field]: "whatever" } } } } as never),
      ),
    ];

    expect(() =>
      evaluate(request({ env: "", resources: [{ type: "network", name: "production" }] }), modules),
    ).toThrow(new RegExp(`aws\\.network\\.${key}\\.${field}`));
  });

  it("patches the tier's network with what ocel leaves open", () => {
    const modules = [
      module(
        "./network.transform.ts",
        defineTransform({ aws: { network: { vpc: { enableNetworkAddressUsageMetrics: true } } } }),
      ),
    ];

    expect(
      evaluate(request({ env: "", resources: [{ type: "network", name: "production" }] }), modules)
        .resources[0]?.patches,
    ).toEqual({ vpc: { enableNetworkAddressUsageMetrics: true } });
  });

  it("sets the block of the tier's network, which ocel carves its subnets from", () => {
    const modules = [
      module(
        "./network.transform.ts",
        defineTransform({ aws: { network: { vpc: { cidrBlock: "172.20.0.0/16" } } } }),
      ),
    ];

    expect(
      evaluate(request({ env: "", resources: [{ type: "network", name: "production" }] }), modules)
        .resources[0]?.patches,
    ).toEqual({ vpc: { cidrBlock: "172.20.0.0/16" } });
  });

  it("returns a patch for every resource the aws provider constructs", () => {
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
          if: (ctx) => ctx.envTier === "preview",
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
          seen = [inputs.envTier, inputs.env];
          return { aws: {} };
        }),
      ),
    ];

    evaluate(request({ envTier: "preview", env: "pr-12" }), modules);

    expect(seen).toEqual(["preview", "pr-12"]);
  });

  it("unions the tags a rule sets and refuses ocel's own prefix", () => {
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
