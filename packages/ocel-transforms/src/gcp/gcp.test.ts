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

  it("hands a topic's and a task's Pub/Sub topic patch to the provider under each kind", () => {
    const modules = [
      module(
        "./queues.transform.ts",
        defineTransform({
          gcp: {
            topic: { topic: { messageRetentionDuration: "86400s" } },
            task: {
              topic: { kmsKeyName: "projects/acme/locations/europe-west1/keyRings/r/cryptoKeys/k" },
            },
          },
        }),
      ),
    ];
    const asked: EvaluateRequest = {
      ...request(),
      resources: [
        { type: "topic", name: "orders" },
        { type: "task", name: "resize" },
      ],
    };

    expect(evaluate(asked, modules).resources.map((resource) => resource.patches)).toEqual([
      { topic: { messageRetentionDuration: "86400s" } },
      { topic: { kmsKeyName: "projects/acme/locations/europe-west1/keyRings/r/cryptoKeys/k" } },
    ]);
  });

  it("refuses a topic field ocel sets itself, by name", () => {
    const modules = [
      module(
        "./owned.transform.ts",
        defineTransform({
          gcp: { topic: { topic: { schemaSettings: { schema: "s" } } as never } },
        }),
      ),
    ];

    expect(() =>
      evaluate({ ...request(), resources: [{ type: "topic", name: "orders" }] }, modules),
    ).toThrow(/gcp\.topic\.topic\.schemaSettings/);
  });

  it("refuses a dead-letter policy on a subscription, by name", () => {
    const modules = [
      module(
        "./dead.transform.ts",
        defineTransform({
          gcp: {
            task: {
              subscription: {
                deadLetterPolicy: { deadLetterTopic: "projects/acme/topics/dead" },
              } as never,
            },
          },
        }),
      ),
    ];

    expect(() =>
      evaluate({ ...request(), resources: [{ type: "task", name: "resize" }] }, modules),
    ).toThrow(/gcp\.task\.subscription\.deadLetterPolicy/);
  });
});

describe("the gcp branch for a database and a store", () => {
  function requesting(type: string, name: string): EvaluateRequest {
    return {
      provider: "gcp",
      envTier: "production",
      env: "production",
      resources: [{ type, name }],
    };
  }

  it("hands the provider a database's Cloud SQL instance settings", () => {
    const modules = [
      module(
        "./database.transform.ts",
        defineTransform({
          gcp: {
            postgres: {
              instance: {
                settings: {
                  tier: "db-custom-2-7680",
                  databaseFlags: [{ name: "log_min_duration_statement", value: "500" }],
                  ipConfiguration: {
                    pscConfigs: [{ pscAutoConnections: [{ consumerNetwork: "shared" }] }],
                  },
                },
              },
            },
          },
        }),
      ),
    ];

    expect(evaluate(requesting("postgres", "orders"), modules).resources[0]?.patches).toEqual({
      instance: {
        settings: {
          tier: "db-custom-2-7680",
          databaseFlags: [{ name: "log_min_duration_statement", value: "500" }],
          ipConfiguration: {
            pscConfigs: [{ pscAutoConnections: [{ consumerNetwork: "shared" }] }],
          },
        },
      },
    });
  });

  it("refuses a database setting ocel owns, by name", () => {
    const modules = [
      module(
        "./owned.transform.ts",
        defineTransform({
          gcp: {
            postgres: {
              instance: { settings: { ipConfiguration: { ipv4Enabled: true } } } as never,
            },
          },
        }),
      ),
    ];

    expect(() => evaluate(requesting("postgres", "orders"), modules)).toThrow(
      /gcp\.postgres\.instance\.settings\.ipConfiguration\.ipv4Enabled/,
    );
  });

  it("hands the provider a store's replicas and the network it is placed on", () => {
    const modules = [
      module(
        "./store.transform.ts",
        defineTransform({
          gcp: {
            kv: {
              instance: { replicaCount: 2, desiredAutoCreatedEndpoints: [{ network: "shared" }] },
            },
          },
        }),
      ),
    ];

    expect(evaluate(requesting("kv", "cache"), modules).resources[0]?.patches).toEqual({
      instance: { replicaCount: 2, desiredAutoCreatedEndpoints: [{ network: "shared" }] },
    });
  });

  it("refuses a store setting ocel owns, by name", () => {
    const modules = [
      module(
        "./owned.transform.ts",
        defineTransform({ gcp: { kv: { instance: { nodeType: "STANDARD_SMALL" } as never } } }),
      ),
    ];

    expect(() => evaluate(requesting("kv", "cache"), modules)).toThrow(
      /gcp\.kv\.instance\.nodeType/,
    );
  });
});
