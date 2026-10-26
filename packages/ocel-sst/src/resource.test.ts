import { spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  custom,
  customProvider,
  postgres,
  postgresProvider,
  variables,
  variablesProvider,
} from "./resource.js";

vi.mock("node:child_process", () => {
  const spawnSync = vi.fn();
  return { spawnSync, default: { spawnSync } };
});

const run = vi.mocked(spawnSync);

const ocelCLI = "/usr/local/bin/ocel";

const root = "/repo/app";

const properties = {
  host: "orders.cluster-c.us-east-1.rds.amazonaws.com",
  port: 5432,
  database: "orders",
  username: "operator",
  password: "hunter2",
};

interface Built {
  name: string;
  props: Record<string, unknown>;
  opts: Record<string, unknown> | undefined;
}

const built: Built[] = [];

class FakeResource {
  constructor(
    _provider: unknown,
    name: string,
    props: Record<string, unknown>,
    opts?: Record<string, unknown>,
  ) {
    built.push({ name, props, opts });
  }
}

const secretValues = new Set<unknown>();

beforeEach(() => {
  built.length = 0;
  secretValues.clear();
  run.mockReturnValue({ status: 0, stderr: "" } as never);
  vi.stubEnv("OCEL_CLI", ocelCLI);
  Object.assign(globalThis, {
    $util: {
      getStack: () => "production",
      getProject: () => "shop",
      createUrn: (name: string, type: string, parent: { urn: string }) =>
        `${parent.urn}$${type}::${name}`,
      output: (value: unknown) => value,
      isSecret: async (value: unknown) => secretValues.has(value),
      dynamic: { Resource: FakeResource },
    },
    $cli: { paths: { root } },
  });
});

afterEach(() => {
  vi.clearAllMocks();
  vi.restoreAllMocks();
  vi.unstubAllEnvs();
  Reflect.deleteProperty(globalThis, "$util");
  Reflect.deleteProperty(globalThis, "$cli");
});

function declare(opts?: Parameters<typeof postgres>[2]) {
  postgres("orders", { getSSTLink: () => ({ properties }) }, opts);
  return latest();
}

function latest(): Built {
  const one = built[built.length - 1];
  if (!one) throw new Error("no dynamic resource was built");
  return one;
}

function written(...keys: string[]): string {
  const values = keys.map((key) => ({ coordinate: { key }, version: "1" }));
  return `${JSON.stringify({ ok: true, data: { written: values } })}\n`;
}

function argv() {
  const call = run.mock.calls[0];
  if (!call) throw new Error("the ocel CLI was never run");
  return { command: call[0], args: call[1] as string[], options: call[2] };
}

describe("declaring a postgres binding", () => {
  it("builds one resource per call, owned by that resource alone", () => {
    const one = declare();

    expect(one.name).toBe("ocel-binding-orders");
    expect(one.props).toMatchObject({
      name: "orders",
      tier: "production",
      directory: root,
      owner:
        "urn:pulumi:production::shop::pulumi:pulumi:Stack$pulumi-nodejs:dynamic:Resource::ocel-binding-orders",
      properties,
    });
  });

  it("takes the ocel project from the sst config root unless it is given", () => {
    expect(declare().props).toMatchObject({ project: ".", directory: root });
    expect(declare({ project: "/repo/other" }).props).toMatchObject({
      project: "../other",
      directory: "/repo/other",
    });
  });

  it("owns the binding by the urn it has under the parent it hangs from", () => {
    const parent = {
      urn: "urn:pulumi:production::shop::pulumi:pulumi:Stack$aws:rds/instance:Instance::db",
    };

    const one = declare({ parent });

    expect(one.opts).toMatchObject({ parent });
    expect(one.props.owner).toBe(
      "urn:pulumi:production::shop::pulumi:pulumi:Stack$aws:rds/instance:Instance::db$pulumi-nodejs:dynamic:Resource::ocel-binding-orders",
    );
  });

  it("resolves a grant's resource from an output before it publishes", async () => {
    const resourceArn = "arn:aws:rds-db:us-east-1:111122223333:dbuser:cluster-abc/operator";
    postgres("orders", {
      ...properties,
      grants: [{ actions: ["rds-db:connect"], resources: [Promise.resolve(resourceArn)] }],
    });
    const one = latest();
    const grants = one.props.grants as { actions: string[]; resources: Promise<string>[] }[];
    const resolved = {
      ...one.props,
      grants: await Promise.all(
        grants.map(async (grant) => ({ ...grant, resources: await Promise.all(grant.resources) })),
      ),
    };

    await postgresProvider.create(resolved as never);

    expect(JSON.parse(String(argv().options?.input))).toMatchObject({
      grants: [{ actions: ["rds-db:connect"], resources: [resourceArn] }],
    });
  });

  it("refuses an environment outside the preview tier", () => {
    expect(() => declare({ environment: "pr-12" })).toThrow(/is named alongside tier production/);
  });

  it("refuses the reserved tier-wide marker", () => {
    expect(() => declare({ tier: "preview", environment: "*" })).toThrow(/reserved/);
  });

  it("says where a binding call belongs when nothing supplies the util", () => {
    Reflect.deleteProperty(globalThis, "$util");

    expect(() => declare()).toThrow(/Move the binding call into run\(\)/);
  });

  it("says where a binding call belongs when nothing names the sst config root", () => {
    Reflect.deleteProperty(globalThis, "$cli");

    expect(() => declare()).toThrow(/bind from inside the run\(\) of your sst.config.ts/);
  });

  it("keeps only the fields a postgres binding contains", () => {
    postgres("orders", {
      getSSTLink: () => ({ properties: { ...properties, extra: "dropped" } }),
    });

    expect(built[0]?.props.properties).toEqual(properties);
  });
});

describe("publishing a postgres binding", () => {
  it("runs ocel bindings set in the project, owned by this resource", async () => {
    const created = await postgresProvider.create(declare().props as never);

    const { command, args, options } = argv();
    expect(command).toBe(ocelCLI);
    expect(args).toEqual([
      "bindings",
      "set",
      "--owner",
      "urn:pulumi:production::shop::pulumi:pulumi:Stack$pulumi-nodejs:dynamic:Resource::ocel-binding-orders",
      "--json",
    ]);
    expect(options).toMatchObject({ cwd: root });
    expect(JSON.parse(String(options?.input))).toEqual({
      binding: { name: "orders", postgres: properties, source: "sst" },
    });
    expect(created.id).toBe("production/orders");
  });

  it("names the preview environment it publishes into", async () => {
    await postgresProvider.create(
      declare({ tier: "preview", environment: "pr-12" }).props as never,
    );

    expect(argv().args.slice(-3)).toEqual(["--preview", "--environment", "pr-12"]);
  });

  it("binds tier-wide when no preview environment is named", async () => {
    await postgresProvider.create(declare({ tier: "preview" }).props as never);

    expect(argv().args.slice(-1)).toEqual(["--preview"]);
  });

  it("outputs a digest and never a property", async () => {
    const created = await postgresProvider.create(declare().props as never);

    expect(created.outs.digest).toMatch(/^[0-9a-f]{64}$/);
    expect(JSON.stringify(created.outs)).not.toContain(properties.password);
  });

  it("surfaces the CLI's refusal verbatim", async () => {
    run.mockReturnValue({
      status: 1,
      stderr: "binding orders in production is already published by publisher urn:pulumi:other\n",
    } as never);

    await expect(postgresProvider.create(declare().props as never)).rejects.toThrow(
      "binding orders in production is already published by publisher urn:pulumi:other",
    );
  });
});

describe("binding an sst component", () => {
  class Aurora {
    getSSTLink() {
      return {
        properties: {
          clusterArn: "arn:aws:rds:us-east-1:1:cluster:orders",
          secretArn: "arn:aws:secretsmanager:us-east-1:1:secret:orders",
          ...properties,
          reader: "orders.cluster-ro-c.us-east-1.rds.amazonaws.com",
        },
        include: [
          {
            type: "aws.permission" as const,
            actions: ["secretsmanager:GetSecretValue"],
            resources: ["arn:aws:secretsmanager:us-east-1:1:secret:orders"],
          },
          {
            type: "aws.permission" as const,
            actions: ["rds-data:ExecuteStatement"],
            resources: ["arn:aws:rds:us-east-1:1:cluster:orders"],
          },
        ],
      };
    }
  }

  it("publishes the properties and the grants its link includes", async () => {
    postgres("orders", new Aurora());
    await postgresProvider.create(latest().props as never);

    expect(JSON.parse(String(argv().options?.input))).toEqual({
      binding: { name: "orders", postgres: properties, source: "sst" },
      grants: [
        {
          actions: ["secretsmanager:GetSecretValue"],
          resources: ["arn:aws:secretsmanager:us-east-1:1:secret:orders"],
        },
        {
          actions: ["rds-data:ExecuteStatement"],
          resources: ["arn:aws:rds:us-east-1:1:cluster:orders"],
        },
      ],
    });
  });
});

describe("changing a published postgres binding", () => {
  it("updates in place when only the record changed", async () => {
    const olds = (await postgresProvider.create(declare().props as never)).outs;
    postgres("orders", {
      getSSTLink: () => ({ properties: { ...properties, host: "moved" } }),
    });

    const diff = await postgresProvider.diff("id", olds, latest().props as never);

    expect(diff).toMatchObject({ changes: true, replaces: [] });
  });

  it("updates in place when the same project is applied from another checkout", async () => {
    Object.assign(globalThis, { $cli: { paths: { root: "/laptop/shop" } } });
    const olds = (await postgresProvider.create(declare().props as never)).outs;
    Object.assign(globalThis, { $cli: { paths: { root: "/ci/work/shop" } } });

    const diff = await postgresProvider.diff("id", olds, declare().props as never);

    expect(diff).toMatchObject({ changes: false, replaces: [] });
  });

  it("replaces when the project it lands in changed", async () => {
    const olds = (await postgresProvider.create(declare().props as never)).outs;

    const diff = await postgresProvider.diff(
      "id",
      olds,
      declare({ project: "/repo/other" }).props as never,
    );

    expect(diff.replaces).toContain("project");
  });

  it("replaces when the tier it lands in changed", async () => {
    const olds = (await postgresProvider.create(declare().props as never)).outs;

    const diff = await postgresProvider.diff(
      "id",
      olds,
      declare({ tier: "preview" }).props as never,
    );

    expect(diff.replaces).toContain("tier");
    expect(diff.deleteBeforeReplace).toBe(true);
  });

  it("reports a change rather than throwing while a property is still unknown", async () => {
    const olds = (await postgresProvider.create(declare().props as never)).outs;
    postgres("orders", {
      getSSTLink: () => ({
        properties: { ...properties, host: undefined },
      }),
    });

    expect(await postgresProvider.diff("id", olds, latest().props as never)).toMatchObject({
      changes: true,
      replaces: [],
    });
  });

  it("reports no change when nothing changed", async () => {
    const olds = (await postgresProvider.create(declare().props as never)).outs;

    expect(await postgresProvider.diff("id", olds, declare().props as never)).toMatchObject({
      changes: false,
      replaces: [],
    });
  });
});

describe("removing a postgres binding", () => {
  it("runs ocel bindings rm for the name it published", async () => {
    const created = await postgresProvider.create(declare().props as never);
    run.mockClear();

    await postgresProvider.delete("id", created.outs);

    expect(argv().args).toEqual(["bindings", "rm", "orders", "--json"]);
    expect(argv().options).toMatchObject({ cwd: root });
  });

  it("runs ocel bindings rm in the project under the checkout removing it", async () => {
    const checkout = mkdtempSync(join(tmpdir(), "ocel-sst-"));
    writeFileSync(join(checkout, "sst.config.ts"), "");
    const platform = join(checkout, ".sst", "platform");
    mkdirSync(platform, { recursive: true });
    Object.assign(globalThis, { $cli: { paths: { root: "/laptop/shop" } } });
    const created = await postgresProvider.create(declare().props as never);
    run.mockClear();
    Reflect.deleteProperty(globalThis, "$cli");
    vi.spyOn(process, "cwd").mockReturnValue(platform);

    await postgresProvider.delete("id", created.outs);

    expect(argv().options).toMatchObject({ cwd: checkout });
  });
});

describe("declaring a custom binding", () => {
  const network = {
    subnetIds: ["subnet-0a1", "subnet-0b2"],
    securityGroupIds: ["sg-0c3"],
  };

  function declareCustom(opts?: Parameters<typeof custom>[2]) {
    custom("network", network, opts);
    return latest();
  }

  it("builds one resource per call, owned by that resource alone", () => {
    const one = declareCustom();

    expect(one.name).toBe("ocel-binding-network");
    expect(one.props).toMatchObject({
      name: "network",
      tier: "production",
      directory: root,
      owner:
        "urn:pulumi:production::shop::pulumi:pulumi:Stack$pulumi-nodejs:dynamic:Resource::ocel-binding-network",
      properties: network,
    });
  });

  it("runs ocel bindings set with a custom record sourced to sst", async () => {
    const created = await customProvider.create(declareCustom().props as never);

    const { args, options } = argv();
    expect(args).toEqual([
      "bindings",
      "set",
      "--owner",
      "urn:pulumi:production::shop::pulumi:pulumi:Stack$pulumi-nodejs:dynamic:Resource::ocel-binding-network",
      "--json",
    ]);
    expect(JSON.parse(String(options?.input))).toEqual({
      binding: { name: "network", custom: network, source: "sst" },
    });
    expect(created.id).toBe("production/network");
  });

  it("reports a change rather than throwing while a property is still unknown", async () => {
    const olds = (await customProvider.create(declareCustom().props as never)).outs;
    custom("network", { ...network, subnetIds: undefined });

    expect(await customProvider.diff("id", olds, latest().props as never)).toMatchObject({
      changes: true,
      replaces: [],
    });
  });

  it("reports no change when nothing changed", async () => {
    const olds = (await customProvider.create(declareCustom().props as never)).outs;

    expect(await customProvider.diff("id", olds, declareCustom().props as never)).toMatchObject({
      changes: false,
      replaces: [],
    });
  });

  it("runs ocel bindings rm for the name it published", async () => {
    const created = await customProvider.create(declareCustom().props as never);
    run.mockClear();

    await customProvider.delete("id", created.outs);

    expect(argv().args).toEqual(["bindings", "rm", "network", "--json"]);
  });
});

describe("publishing variables", () => {
  const stack = {};

  const publisher = "urn:pulumi:production::shop";

  beforeEach(() => {
    run.mockReturnValue({
      status: 0,
      stdout: written("API_KEY", "DATABASE_URL"),
      stderr: "",
    } as never);
  });

  const apiKey = "k3y";

  function declareVariables(
    values: Parameters<typeof variables>[0] = {
      API_KEY: apiKey,
      DATABASE_URL: "postgres://db.internal:5432/app",
    },
    opts?: Parameters<typeof variables>[1],
  ): Built {
    secretValues.add(apiKey);
    variables(values, opts);
    return latest();
  }

  async function inputs(one: Built) {
    return { ...one.props, secrets: await one.props.secrets } as never;
  }

  it("builds one resource per coordinate, publishing as the program it runs in", async () => {
    const one = declareVariables();

    expect(one.name).toBe("ocel-variables-production");
    expect(await inputs(one)).toMatchObject({
      tier: "production",
      directory: root,
      publisher,
      values: { API_KEY: apiKey, DATABASE_URL: "postgres://db.internal:5432/app" },
      secrets: ["API_KEY"],
    });
  });

  it("publishes as the program wherever the call hangs, so moving the call keeps its values its own", async () => {
    const parent = {
      urn: "urn:pulumi:production::shop::pulumi:pulumi:Stack$my:component:Api::api",
    };

    expect((await inputs(declareVariables(undefined, { parent }))).publisher).toBe(publisher);
  });

  it("refuses a second call for one coordinate, which would remove what the first published", () => {
    Object.assign(globalThis.$util as object, { runtime: { getStackResource: () => stack } });
    declareVariables();

    expect(() => declareVariables()).toThrow(/bind.variables is called twice for production/);
  });

  it("names the resource for a preview environment after it", () => {
    expect(declareVariables(undefined, { tier: "preview", environment: "pr-12" }).name).toBe(
      "ocel-variables-preview-pr-12",
    );
  });

  it("keeps its digest secret, since a digest of a secret is a guess at it", () => {
    expect(declareVariables().opts).toMatchObject({ additionalSecretOutputs: ["digest"] });
  });

  it("refuses to publish when SST supplies no way to tell a secret output", () => {
    Reflect.deleteProperty(globalThis.$util as object, "isSecret");

    expect(() => declareVariables()).toThrow(/\$util.isSecret/);
    expect(built).toHaveLength(0);
  });

  it("runs ocel env publish in the project as this resource, marking the secret output", async () => {
    const created = await variablesProvider.create(await inputs(declareVariables()));

    const { command, args, options } = argv();
    expect(command).toBe(ocelCLI);
    expect(args).toEqual(["env", "publish", "--publisher", publisher, "--json"]);
    expect(options).toMatchObject({ cwd: root });
    expect(JSON.parse(String(options?.input))).toEqual({
      values: [
        { key: "API_KEY", value: apiKey, secret: true },
        { key: "DATABASE_URL", value: "postgres://db.internal:5432/app" },
      ],
    });
    expect(created.id).toBe("variables/production");
    expect(created.outs).toMatchObject({ versions: { API_KEY: "1", DATABASE_URL: "1" } });
    expect(JSON.stringify(created.outs)).not.toContain(apiKey);
  });

  it("names the preview environment it publishes into", async () => {
    await variablesProvider.create(
      await inputs(declareVariables(undefined, { tier: "preview", environment: "pr-12" })),
    );

    expect(argv().args.slice(-3)).toEqual(["--preview", "--environment", "pr-12"]);
  });

  it("publishes the whole set again when a value changes, which drops a name it stopped listing", async () => {
    const olds = (await variablesProvider.create(await inputs(declareVariables()))).outs;
    const news = await inputs(declareVariables({ API_KEY: apiKey }));
    run.mockClear();

    expect(await variablesProvider.diff("id", olds, news)).toMatchObject({
      changes: true,
      replaces: [],
    });
    await variablesProvider.update("id", olds, news);

    expect(JSON.parse(String(argv().options?.input))).toEqual({
      values: [{ key: "API_KEY", value: apiKey, secret: true }],
    });
  });

  it("reports no change when nothing changed", async () => {
    const olds = (await variablesProvider.create(await inputs(declareVariables()))).outs;

    expect(
      await variablesProvider.diff("id", olds, await inputs(declareVariables())),
    ).toMatchObject({ changes: false, replaces: [] });
  });

  it("reports a change rather than throwing while a value is still unknown", async () => {
    const olds = (await variablesProvider.create(await inputs(declareVariables()))).outs;

    expect(
      await variablesProvider.diff(
        "id",
        olds,
        await inputs(declareVariables({ API_KEY: undefined })),
      ),
    ).toMatchObject({ changes: true, replaces: [] });
  });

  it("replaces when the coordinate it publishes to changed", async () => {
    const olds = (await variablesProvider.create(await inputs(declareVariables()))).outs;

    const diff = await variablesProvider.diff(
      "id",
      olds,
      await inputs(declareVariables(undefined, { tier: "preview" })),
    );

    expect(diff.replaces).toContain("tier");
    expect(diff.deleteBeforeReplace).toBe(true);
  });

  it("refuses a publish whose result names no version for a value it wrote", async () => {
    run.mockReturnValue({ status: 0, stdout: written("API_KEY"), stderr: "" } as never);

    await expect(variablesProvider.create(await inputs(declareVariables()))).rejects.toThrow(
      /DATABASE_URL/,
    );
  });

  it("removes, when it is deleted, only the values still at the versions it wrote", async () => {
    const created = await variablesProvider.create(await inputs(declareVariables()));
    run.mockClear();

    await variablesProvider.delete("id", created.outs);

    expect(argv().args).toEqual(["env", "unpublish", "--publisher", publisher, "--json"]);
    expect(argv().options).toMatchObject({ cwd: root });
    expect(JSON.parse(String(argv().options?.input))).toEqual({
      values: [
        { key: "API_KEY", version: "1" },
        { key: "DATABASE_URL", version: "1" },
      ],
    });
  });
});
