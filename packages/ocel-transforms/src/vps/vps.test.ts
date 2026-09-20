import { describe, expect, it } from "vitest";
import { defineTransform } from "../define";
import { type EvaluateRequest, evaluate, type TransformModule } from "../evaluate";

const pgvector =
  "public.ecr.aws/docker/library/postgres@sha256:00bc86618629af00d2937fdc5a5d63db3ff8450acf52f0636ec813c7f4902929";

function request(
  resources: EvaluateRequest["resources"] = [{ type: "postgres", name: "main" }],
): EvaluateRequest {
  return {
    provider: "vps",
    envClass: "production",
    env: "production",
    resources,
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

  it("hands the provider the store a rule reshaped, which the box runs like any other", () => {
    const modules = [
      module(
        "./store.transform.ts",
        defineTransform({
          vps: { bucket: { volume: { driverOpts: { device: "/mnt/objects" } } } },
        }),
      ),
    ];
    const asked = request([{ type: "bucket", name: "uploads" }]);

    expect(evaluate(asked, modules).resources[0]).toEqual({
      name: "uploads",
      patches: { volume: { driverOpts: { device: "/mnt/objects" } } },
      tags: {},
    });
  });

  it("refuses a patch of what the box fills itself on a store", () => {
    const modules = [
      module(
        "./owned-store.transform.ts",
        defineTransform({ vps: { bucket: { container: { publish: ["9000:9000"] } } } } as never),
      ),
    ];
    const asked = request([{ type: "bucket", name: "uploads" }]);

    expect(() => evaluate(asked, modules)).toThrow(/vps\.bucket\.container\.publish/);
  });

  it("refuses a module that only ever patched aws", () => {
    const modules = [module("./aws.transform.ts", defineTransform({ aws: {} }))];

    expect(() => evaluate(request(), modules)).toThrow(
      /\.\/aws\.transform\.ts patches aws and this project deploys to vps/,
    );
  });
});
