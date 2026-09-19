import { describe, expect, it } from "vitest";
import { defineTransform } from "../define";
import { type EvaluateRequest, evaluate, type TransformModule } from "../evaluate";

const pgvector =
  "public.ecr.aws/docker/library/postgres@sha256:00bc86618629af00d2937fdc5a5d63db3ff8450acf52f0636ec813c7f4902929";

function request(): EvaluateRequest {
  return {
    provider: "vps",
    envClass: "production",
    env: "production",
    resources: [{ type: "postgres", name: "main" }],
  };
}

function module(
  specifier: string,
  definition: ReturnType<typeof defineTransform>,
): TransformModule {
  return { specifier, definition };
}

describe("the vps branch", () => {
  it("hands the provider the stack a rule reshaped, keyed by what the box runs for it", () => {
    const modules = [
      module(
        "./stack.transform.ts",
        defineTransform({
          vps: {
            postgres: {
              container: { image: pgvector, args: ["-c", "max_connections=200"], memory: "2g" },
              volume: {
                driver: "local",
                driverOpts: { type: "none", o: "bind", device: "/mnt/pg" },
              },
            },
          },
        }),
      ),
    ];

    expect(evaluate(request(), modules).resources[0]).toEqual({
      name: "main",
      patches: {
        container: { image: pgvector, args: ["-c", "max_connections=200"], memory: "2g" },
        volume: { driver: "local", driverOpts: { type: "none", o: "bind", device: "/mnt/pg" } },
      },
      tags: {},
    });
  });

  it.each([
    ["container", "name", "mine"],
    ["container", "network", "host"],
    ["container", "labels", { "ocel.project": "theirs" }],
    ["container", "publish", ["5432:5432"]],
    ["container", "mounts", ["/:/host"]],
    ["volume", "name", "mine"],
    ["volume", "labels", { "ocel.class": "production" }],
  ])("refuses %s.%s, which the box fills itself", (surface, field, value) => {
    const modules = [
      module(
        "./owned.transform.ts",
        defineTransform({ vps: { postgres: { [surface]: { [field]: value } } } } as never),
      ),
    ];

    expect(() => evaluate(request(), modules)).toThrow(
      new RegExp(`vps\\.postgres\\.${surface}\\.${field}`),
    );
  });

  it("refuses a module that only ever patched aws", () => {
    const modules = [module("./aws.transform.ts", defineTransform({ aws: {} }))];

    expect(() => evaluate(request(), modules)).toThrow(
      /\.\/aws\.transform\.ts patches aws and this project deploys to vps/,
    );
  });
});
