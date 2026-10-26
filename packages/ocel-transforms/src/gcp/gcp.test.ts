import { describe, expect, it } from "vitest";
import { defineTransform } from "../define.js";
import { type EvaluateRequest, evaluate, type TransformModule } from "../evaluate.js";
import { bindings } from "../output.js";

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

  it("hands the provider the Cloud Run service patch of the app a gate picks", () => {
    const modules = [
      module(
        "./run.transform.ts",
        defineTransform([
          {
            if: (ctx) => ctx.app === "web",
            gcp: {
              function: {
                service: {
                  template: {
                    containers: [
                      {
                        resources: { limits: { cpu: "2", "nvidia.com/gpu": "1" }, cpuIdle: false },
                      },
                    ],
                    serviceAccount: "web@acme.iam.gserviceaccount.com",
                    timeout: "900s",
                  },
                },
              },
            },
          },
        ]),
      ),
    ];
    const asked: EvaluateRequest = {
      ...request(),
      resources: [
        { type: "function", name: "fn--web--index", app: "web" },
        { type: "function", name: "fn--api--index", app: "api" },
      ],
    };

    const evaluated = evaluate(asked, modules).resources;

    expect(evaluated[0]?.patches).toEqual({
      service: {
        template: {
          containers: [
            { resources: { limits: { cpu: "2", "nvidia.com/gpu": "1" }, cpuIdle: false } },
          ],
          serviceAccount: "web@acme.iam.gserviceaccount.com",
          timeout: "900s",
        },
      },
    });
    expect(evaluated[1]?.patches).toEqual({});
  });

  it("refuses a worker's timeout, which ocel keeps as the push deadline", () => {
    const modules = [
      module(
        "./worker.transform.ts",
        defineTransform({
          gcp: { worker: { service: { template: { timeout: "60s" } } as never } },
        }),
      ),
    ];
    const asked: EvaluateRequest = {
      ...request(),
      resources: [{ type: "worker", name: "media", app: "web" }],
    };

    expect(() => evaluate(asked, modules)).toThrow(/gcp\.worker\.service\.template\.timeout/);
  });

  it("hands the provider the tier network's patches, and refuses a field ocel keeps", () => {
    const tier: EvaluateRequest = {
      provider: "gcp",
      envTier: "production",
      env: "",
      resources: [{ type: "network", name: "production" }],
    };
    const patched = module(
      "./network.transform.ts",
      defineTransform({
        gcp: {
          network: { subnetwork: { ipCidrRange: "10.96.0.0/20", privateIpGoogleAccess: true } },
        },
      }),
    );
    const owned = module(
      "./owned.transform.ts",
      defineTransform({ gcp: { network: { subnetwork: { region: "us-east1" } as never } } }),
    );

    expect(evaluate(tier, [patched]).resources[0]?.patches).toEqual({
      subnetwork: { ipCidrRange: "10.96.0.0/20", privateIpGoogleAccess: true },
    });
    expect(() => evaluate(tier, [owned])).toThrow(/gcp\.network\.subnetwork\.region/);
  });

  it("hands the provider an empty object over paths it translates, which turns flow logs on at Google's defaults", () => {
    const tier: EvaluateRequest = {
      provider: "gcp",
      envTier: "production",
      env: "",
      resources: [{ type: "network", name: "production" }],
    };
    const logged = module(
      "./logs.transform.ts",
      defineTransform({ gcp: { network: { subnetwork: { logConfig: {} } } } }),
    );

    expect(evaluate(tier, [logged]).resources[0]?.patches).toEqual({
      subnetwork: { logConfig: {} },
    });
  });

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

describe("the gcp branch over the load balancer ocel provisions with Pulumi", () => {
  function edgeRequest(): EvaluateRequest {
    return {
      provider: "gcp",
      envTier: "production",
      env: "",
      resources: [{ type: "edge", name: "web", app: "web" }],
    };
  }

  function frontRequest(): EvaluateRequest {
    return {
      provider: "gcp",
      envTier: "production",
      env: "",
      resources: [{ type: "loadBalancer", name: "production" }],
    };
  }

  it("hands the provider an app's backend service patch, in the args @pulumi/gcp takes", () => {
    const modules = [
      module(
        "./armor.transform.ts",
        defineTransform({
          gcp: {
            edge: {
              backendService: {
                securityPolicy: "web-armor",
                timeoutSec: 120,
                logConfig: { enable: true, sampleRate: 0.5 },
                cdnPolicy: { serveWhileStale: 86400 },
              },
            },
          },
        }),
      ),
    ];

    expect(evaluate(edgeRequest(), modules).resources[0]?.patches).toEqual({
      backendService: {
        securityPolicy: "web-armor",
        timeoutSec: 120,
        logConfig: { enable: true, sampleRate: 0.5 },
        cdnPolicy: { serveWhileStale: 86400 },
      },
    });
  });

  it.each([
    ["backendService", { name: "mine" }, "backendService.name"],
    ["backendService", { backends: [] }, "backendService.backends"],
    [
      "backendService",
      { cdnPolicy: { cacheMode: "CACHE_ALL_STATIC" } },
      "backendService.cdnPolicy.cacheMode",
    ],
    ["backendService", { cdnPolicy: null }, "backendService.cdnPolicy.bypassCacheOnRequestHeaders"],
    [
      "backendService",
      { cdnPolicy: bindings.custom!.edge!.policy },
      "backendService.cdnPolicy.bypassCacheOnRequestHeaders",
    ],
    ["backendService", { cdnPolicy: [] }, "backendService.cdnPolicy.bypassCacheOnRequestHeaders"],
    ["neg", { cloudRun: { service: "other" } }, "neg.cloudRun"],
    ["certificateMapEntry", { hostname: "other.example.com" }, "certificateMapEntry.hostname"],
  ])("refuses %s %o, which routing depends on", (key, patch, named) => {
    const modules = [
      module("./owned.transform.ts", defineTransform({ gcp: { edge: { [key]: patch } } } as never)),
    ];

    expect(() => evaluate(edgeRequest(), modules)).toThrow(
      new RegExp(`gcp\\.edge\\.${named.replaceAll(".", "\\.")}`),
    );
  });

  it("keeps a sibling of a nested field ocel owns open", () => {
    const modules = [
      module(
        "./stale.transform.ts",
        defineTransform({
          gcp: { edge: { backendService: { cdnPolicy: { negativeCaching: true } } } },
        }),
      ),
    ];

    expect(evaluate(edgeRequest(), modules).resources[0]?.patches).toEqual({
      backendService: { cdnPolicy: { negativeCaching: true } },
    });
  });

  it("patches the tier's load balancer beside a bucket in one rule", () => {
    const definition = defineTransform({
      gcp: {
        bucket: { bucket: { storageClass: "NEARLINE" } },
        loadBalancer: { httpsProxy: { sslPolicy: "modern-tls", quicOverride: "ENABLE" } },
      },
    });
    const evaluated = evaluate(
      {
        ...frontRequest(),
        resources: [...frontRequest().resources, { type: "bucket", name: "uploads" }],
      },
      [module("./front.transform.ts", definition)],
    );

    expect(evaluated.resources.map((resource) => resource.patches)).toEqual([
      { httpsProxy: { sslPolicy: "modern-tls", quicOverride: "ENABLE" } },
      { bucket: { storageClass: "NEARLINE" } },
    ]);
  });

  it("refuses the url map's host rules, which ocel writes itself", () => {
    const modules = [
      module(
        "./routes.transform.ts",
        defineTransform({ gcp: { loadBalancer: { urlMap: { hostRules: [] } } } } as never),
      ),
    ];

    expect(() => evaluate(frontRequest(), modules)).toThrow(/gcp\.loadBalancer\.urlMap\.hostRules/);
  });
});
