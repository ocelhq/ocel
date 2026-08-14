import { describe, expect, it } from "vitest";
import { defineTransform } from "./define";
import { evaluate, type EvaluateRequest, type TransformModule } from "./evaluate";

function request(
  overrides: Partial<EvaluateRequest> = {},
): EvaluateRequest {
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
        },
      },
    ],
    ...overrides,
  };
}

function transformModule(
  specifier: string,
  rules: ReturnType<typeof defineTransform>,
): TransformModule {
  return { specifier, rules };
}

describe("evaluate", () => {
  it("returns the provider's own surfaces untouched when no module carries a rule", () => {
    const req = request();

    expect(evaluate(req, [])).toEqual({
      resources: [
        { name: "api-users", surfaces: req.resources[0]!.surfaces, tags: {} },
      ],
    });
  });

  it("merges a patch over the defaulted args, leaving unmentioned fields alone", () => {
    const got = evaluate(request(), [
      transformModule(
        "a.ts",
        defineTransform({ function: { lambda: { memorySizeMb: 2048 } } }),
      ),
    ]);

    expect(got.resources[0]!.surfaces.lambda).toEqual({
      memorySizeMb: 2048,
      timeoutSeconds: 30,
      runtime: "nodejs24.x",
    });
  });

  it("applies rules in order within a module, the later rule winning per field", () => {
    const got = evaluate(request(), [
      transformModule(
        "a.ts",
        defineTransform([
          { function: { lambda: { memorySizeMb: 2048, timeoutSeconds: 10 } } },
          { function: { lambda: { memorySizeMb: 512 } } },
        ]),
      ),
    ]);

    expect(got.resources[0]!.surfaces.lambda).toMatchObject({
      memorySizeMb: 512,
      timeoutSeconds: 10,
    });
  });

  it("applies modules in the order the config lists them, the later module winning", () => {
    const got = evaluate(request(), [
      transformModule(
        "first.ts",
        defineTransform({ function: { lambda: { memorySizeMb: 2048 } } }),
      ),
      transformModule(
        "second.ts",
        defineTransform({ function: { lambda: { memorySizeMb: 128 } } }),
      ),
    ]);

    expect(got.resources[0]!.surfaces.lambda).toMatchObject({
      memorySizeMb: 128,
    });
  });

  it("rejects a patch naming a field outside the allowlist, naming module, key and field", () => {
    const rules = defineTransform({
      function: { lambda: { reservedConcurrency: 4 } as never },
    });

    expect(() => evaluate(request(), [transformModule("a.ts", rules)])).toThrow(
      /a\.ts.*function\.lambda\.reservedConcurrency.*not a transformable field/s,
    );
  });

  it("rejects a rule naming an underlying resource the provider does not expose", () => {
    const rules = defineTransform({
      function: { logGroup: { retentionDays: 7 } } as never,
    });

    expect(() => evaluate(request(), [transformModule("a.ts", rules)])).toThrow(
      /a\.ts.*function\.logGroup.*not a transformable/s,
    );
  });

  it("hands an override function the fully-defaulted args and keeps its mutations", () => {
    let seen: unknown;
    const got = evaluate(request(), [
      transformModule(
        "a.ts",
        defineTransform({
          function: {
            lambda: (args) => {
              seen = { ...args };
              args.memorySizeMb = args.memorySizeMb * 2;
            },
          },
        }),
      ),
    ]);

    expect(seen).toEqual({
      memorySizeMb: 1024,
      timeoutSeconds: 30,
      runtime: "nodejs24.x",
    });
    expect(got.resources[0]!.surfaces.lambda).toMatchObject({
      memorySizeMb: 2048,
    });
  });

  it("takes a returned args object in place of the one it handed over", () => {
    const got = evaluate(request(), [
      transformModule(
        "a.ts",
        defineTransform({
          function: { lambda: (args) => ({ ...args, timeoutSeconds: 60 }) },
        }),
      ),
    ]);

    expect(got.resources[0]!.surfaces.lambda).toMatchObject({
      timeoutSeconds: 60,
      memorySizeMb: 1024,
    });
  });

  it("rejects an override that returns args missing an allowlisted field", () => {
    const rules = defineTransform({
      function: { lambda: (() => ({ timeoutSeconds: 60 })) as never },
    });

    expect(() => evaluate(request(), [transformModule("a.ts", rules)])).toThrow(
      /a\.ts.*function\.lambda.*memorySizeMb/s,
    );
  });

  it("skips a rule whose gate returns false", () => {
    const got = evaluate(request(), [
      transformModule(
        "a.ts",
        defineTransform({
          if: (ctx) => ctx.envClass === "preview",
          function: { lambda: { memorySizeMb: 128 } },
        }),
      ),
    ]);

    expect(got.resources[0]!.surfaces.lambda).toMatchObject({
      memorySizeMb: 1024,
    });
  });

  it("shows a gate the ambient context only, never the candidate resource", () => {
    let seen: Record<string, unknown> = {};
    evaluate(request(), [
      transformModule(
        "a.ts",
        defineTransform({
          if: (ctx) => {
            seen = { ...ctx };
            return true;
          },
          function: { lambda: { memorySizeMb: 128 } },
        }),
      ),
    ]);

    expect(seen).toEqual({ envClass: "production", env: "prod", app: "api" });
    expect(Object.keys(seen)).not.toContain("resourceName");
  });

  it("gates a resource shared across apps with an undefined app", () => {
    let seen: unknown = "unset";
    evaluate(
      request({
        resources: [
          {
            type: "bucket",
            name: "uploads",
            surfaces: {
              bucket: { forceDestroy: false },
              cors: {
                allowedOrigins: [],
                allowedMethods: ["PUT"],
                allowedHeaders: ["*"],
                exposeHeaders: ["ETag"],
                maxAgeSeconds: 3600,
              },
              listener: { timeoutSeconds: 30 },
              notification: { events: ["s3:ObjectCreated:*"] },
            },
          },
        ],
      }),
      [
        transformModule(
          "a.ts",
          defineTransform({
            if: (ctx) => {
              seen = ctx.app;
              return ctx.app === "api";
            },
            bucket: { bucket: { forceDestroy: true } },
          }),
        ),
      ],
    );

    expect(seen).toBeUndefined();
  });

  it("names the candidate resource to an override function, which a gate cannot see", () => {
    const got = evaluate(
      request({
        resources: [
          {
            type: "postgres",
            name: "analytics-db",
            surfaces: {
              cluster: {
                engineVersion: "16.4",
                minCapacity: 0,
                maxCapacity: 2,
                deletionProtection: false,
                skipFinalSnapshot: true,
              },
              instance: {
                instanceClass: "db.serverless",
                publiclyAccessible: false,
              },
            },
          },
        ],
      }),
      [
        transformModule(
          "a.ts",
          defineTransform({
            postgres: {
              cluster: (args, ctx) => {
                if (ctx.resourceName === "analytics-db") {
                  args.maxCapacity = 8;
                }
              },
            },
          }),
        ),
      ],
    );

    expect(got.resources[0]!.surfaces.cluster).toMatchObject({
      maxCapacity: 8,
    });
  });

  it("leaves a resource alone when the rule targets another resource type", () => {
    const req = request();
    const got = evaluate(req, [
      transformModule(
        "a.ts",
        defineTransform({ postgres: { cluster: { maxCapacity: 16 } } }),
      ),
    ]);

    expect(got.resources[0]!.surfaces).toEqual(req.resources[0]!.surfaces);
  });

  it("unions tags from every surviving rule into the resource", () => {
    const got = evaluate(request(), [
      transformModule(
        "a.ts",
        defineTransform([
          { tags: { "acme:team": "platform", "acme:env": "unset" } },
          { if: (ctx) => ctx.envClass === "production", tags: { "acme:env": "prod" } },
          { if: (ctx) => ctx.envClass === "preview", tags: { "acme:ephemeral": "yes" } },
        ]),
      ),
    ]);

    expect(got.resources[0]!.tags).toEqual({
      "acme:team": "platform",
      "acme:env": "prod",
    });
  });

  it("rejects a tag under the prefix ocel writes its own tags with", () => {
    const rules = defineTransform({ tags: { "ocel:component": "mine" } });

    expect(() => evaluate(request(), [transformModule("a.ts", rules)])).toThrow(
      /a\.ts.*ocel:component.*reserved/s,
    );
  });

  it("rejects a rule key that is neither a keyword nor a resource this provider renders", () => {
    const rules = defineTransform({
      functions: { lambda: { memorySizeMb: 512 } },
    } as never);

    expect(() => evaluate(request(), [transformModule("a.ts", rules)])).toThrow(
      /a\.ts.*functions/s,
    );
  });

  it("returns one entry per requested resource, in request order", () => {
    const got = evaluate(
      request({
        resources: [
          {
            type: "function",
            name: "one",
            app: "api",
            surfaces: { lambda: {}, url: {} },
          },
          {
            type: "function",
            name: "two",
            app: "web",
            surfaces: { lambda: {}, url: {} },
          },
        ],
      }),
      [],
    );

    expect(got.resources.map((r) => r.name)).toEqual(["one", "two"]);
  });
});
