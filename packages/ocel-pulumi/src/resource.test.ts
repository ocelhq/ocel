import { spawnSync } from "node:child_process";
import { createRequire } from "node:module";
import { output, type Resource, secret } from "@pulumi/pulumi";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  custom,
  customProvider,
  postgres,
  postgresProvider,
  variables,
  variablesProvider,
} from "./resource.js";

interface Built {
  name: string;
  props: Record<string, unknown>;
  opts: Record<string, unknown> | undefined;
}

const { built, FakeResource, running } = vi.hoisted(() => {
  const running: { stack: object | undefined } = { stack: undefined };
  const built: Built[] = [];
  class FakeResource {
    constructor(
      _provider: unknown,
      name: string,
      props: Record<string, unknown>,
      opts: Record<string, unknown> | undefined,
    ) {
      built.push({ name, props, opts });
    }
  }
  return { built, FakeResource, running };
});

vi.mock("@pulumi/pulumi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@pulumi/pulumi")>();
  return {
    ...actual,
    dynamic: { ...actual.dynamic, Resource: FakeResource },
    runtime: { ...actual.runtime, getStackResource: () => running.stack },
  };
});

vi.mock("node:child_process", () => {
  const spawnSync = vi.fn();
  return { spawnSync, default: { spawnSync } };
});

const run = vi.mocked(spawnSync);

const { Struct } = createRequire(createRequire(import.meta.url).resolve("@pulumi/pulumi"))(
  "google-protobuf/google/protobuf/struct_pb",
) as { Struct: { fromJavaScript(value: object): unknown } };

const ocelCLI = "/usr/local/bin/ocel";

const root = "/repo/app";

const ownerUrn = "urn:pulumi:production::shop";

const properties = {
  host: "orders.cluster-c.us-east-1.rds.amazonaws.com",
  port: 5432,
  database: "orders",
  username: "operator",
  password: "hunter2",
};

beforeEach(() => {
  built.length = 0;
  running.stack = undefined;
  run.mockReturnValue({ status: 0, stdout: bindingSet("1"), stderr: "" } as never);
  vi.stubEnv("OCEL_CLI", ocelCLI);
});

afterEach(() => {
  vi.clearAllMocks();
  vi.restoreAllMocks();
  vi.unstubAllEnvs();
});

function unwrap(value: unknown): Promise<unknown> {
  return new Promise((resolve) => {
    output(value).apply(resolve);
  });
}

async function inputs(one: Built) {
  const resolved: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(one.props)) {
    resolved[key] = await unwrap(value);
  }
  return resolved as never;
}

function declare(opts?: Parameters<typeof postgres>[2]): Built {
  postgres("orders", properties, { project: root, ...opts });
  return latest();
}

function latest(): Built {
  const one = built[built.length - 1];
  if (!one) throw new Error("no dynamic resource was built");
  return one;
}

function bindingSet(version: string): string {
  return `${JSON.stringify({ ok: true, data: { version } })}\n`;
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
  it("builds one resource per call, owned by the program it runs in", async () => {
    const one = declare();

    expect(one.name).toMatch(/^ocel-binding-production-orders-[0-9a-f]{8}$/);
    expect(await inputs(one)).toMatchObject({
      name: "orders",
      tier: "production",
      directory: root,
      owner: ownerUrn,
      properties,
    });
  });

  it("takes the ocel project from the directory pulumi runs in unless it is given", () => {
    postgres("orders", properties);

    expect(latest().props).toMatchObject({ project: ".", directory: process.cwd() });
  });

  it("owns the binding as the program wherever the call hangs, so moving the call keeps it", async () => {
    const parent = {
      __pulumiResource: true,
      urn: output("urn:pulumi:production::shop::aws:rds/instance:Instance::db"),
    } as unknown as Resource;

    const one = declare({ parent });

    expect(one.opts).toMatchObject({ parent });
    expect((await inputs(one)).owner).toBe(ownerUrn);
  });

  it("refuses a second call publishing one name to one coordinate", () => {
    running.stack = {};
    declare();

    expect(() => declare()).toThrow(/binding orders is published twice for production/);
  });

  it("names the resource for its coordinate, the same on every run", () => {
    postgres("orders", properties);
    postgres("orders", properties, { tier: "preview", environment: "pr-1" });
    postgres("orders", properties, { tier: "preview", environment: "pr-2" });

    expect(built.map((one) => one.name)).toEqual([
      "ocel-binding-production-orders-dc162db0",
      "ocel-binding-preview-pr-1-orders-805f3dff",
      "ocel-binding-preview-pr-2-orders-8262e6fb",
    ]);
  });

  it("names the resource for its ocel project, so two projects can each take one name", () => {
    running.stack = {};
    postgres("orders", properties, { project: "apps/a" });
    postgres("orders", properties, { project: "apps/b" });

    const [a, b] = built;
    expect(a?.name).not.toBe(b?.name);
  });

  it("names apart two coordinates whose parts join to the same words", () => {
    postgres("orders", properties, { tier: "preview", environment: "pr-1" });
    postgres("1-orders", properties, { tier: "preview", environment: "pr" });

    const [a, b] = built;
    expect(a?.name).not.toBe(b?.name);
  });

  it("refuses an environment outside the preview tier", () => {
    expect(() => declare({ environment: "pr-12" })).toThrow(/is named alongside tier production/);
  });

  it("refuses the reserved tier-wide marker", () => {
    expect(() => declare({ tier: "preview", environment: "*" })).toThrow(/reserved/);
  });

  it("keeps only the fields a postgres binding contains", async () => {
    postgres("orders", { ...properties, extra: "dropped" } as never, {
      project: root,
    });

    expect((await inputs(latest())).properties).toEqual(properties);
  });
});

describe("publishing a postgres binding", () => {
  it("runs ocel bindings set in the project, owned by the program", async () => {
    const created = await postgresProvider.create(await inputs(declare()));

    const { command, args, options } = argv();
    expect(command).toBe(ocelCLI);
    expect(args).toEqual(["bindings", "set", "--owner", ownerUrn, "--json"]);
    expect(options).toMatchObject({ cwd: root });
    expect(JSON.parse(String(options?.input))).toEqual({
      binding: { name: "orders", postgres: properties, source: "pulumi" },
    });
    expect(created.id).toBe("production/orders");
  });

  it("names the preview environment it publishes into", async () => {
    await postgresProvider.create(await inputs(declare({ tier: "preview", environment: "pr-12" })));

    expect(argv().args.slice(-3)).toEqual(["--preview", "--environment", "pr-12"]);
  });

  it("binds tier-wide when no preview environment is named", async () => {
    await postgresProvider.create(await inputs(declare({ tier: "preview" })));

    expect(argv().args.slice(-1)).toEqual(["--preview"]);
  });

  it("outputs a digest and never a property", async () => {
    const created = await postgresProvider.create(await inputs(declare()));

    expect(created.outs.digest).toMatch(/^[0-9a-f]{64}$/);
    expect(JSON.stringify(created.outs)).not.toContain(properties.password);
  });

  it("outputs only what the engine can send back, on the production tier", async () => {
    const created = await postgresProvider.create(await inputs(declare()));

    expect(() => Struct.fromJavaScript(created.outs)).not.toThrow();
  });

  it("resolves a grant's resource from a pulumi output before it publishes", async () => {
    const resourceArn = "arn:aws:rds-db:us-east-1:111122223333:dbuser:cluster-abc/operator";
    postgres(
      "orders",
      {
        ...properties,
        grants: [{ actions: ["rds-db:connect"], resources: [output(resourceArn)] }],
      },
      { project: root },
    );

    const created = await postgresProvider.create(await inputs(latest()));

    expect(JSON.parse(String(argv().options?.input))).toMatchObject({
      grants: [{ actions: ["rds-db:connect"], resources: [resourceArn] }],
    });
    expect(created.id).toBe("production/orders");
  });

  it("surfaces the CLI's refusal verbatim", async () => {
    run.mockReturnValue({
      status: 1,
      stderr: "binding orders in production is already published by publisher urn:pulumi:other\n",
    } as never);

    await expect(postgresProvider.create(await inputs(declare()))).rejects.toThrow(
      "binding orders in production is already published by publisher urn:pulumi:other",
    );
  });
});

describe("recording what a binding publish wrote", () => {
  it("keeps the version the CLI reports writing", async () => {
    run.mockReturnValue({ status: 0, stdout: bindingSet("4"), stderr: "" } as never);

    expect((await postgresProvider.create(await inputs(declare()))).outs).toMatchObject({
      version: "4",
    });
  });

  it("refuses a publish whose result names no version", async () => {
    run.mockReturnValue({ status: 0, stdout: "", stderr: "" } as never);

    await expect(postgresProvider.create(await inputs(declare()))).rejects.toThrow(/no version/);
  });
});

describe("changing a published postgres binding", () => {
  it("updates in place when only the record changed", async () => {
    const olds = (await postgresProvider.create(await inputs(declare()))).outs;
    postgres("orders", { ...properties, host: "moved" }, { project: root });

    const diff = await postgresProvider.diff("id", olds, await inputs(latest()));

    expect(diff).toMatchObject({ changes: true, replaces: [] });
  });

  it("updates in place when the same project is applied from another checkout", async () => {
    vi.spyOn(process, "cwd").mockReturnValue("/laptop/shop");
    postgres("orders", properties);
    const olds = (await postgresProvider.create(await inputs(latest()))).outs;
    vi.spyOn(process, "cwd").mockReturnValue("/ci/work/shop");
    postgres("orders", properties);

    const diff = await postgresProvider.diff("id", olds, await inputs(latest()));

    expect(diff).toMatchObject({ changes: false, replaces: [] });
  });

  it("replaces when the project it lands in changed", async () => {
    const olds = (await postgresProvider.create(await inputs(declare()))).outs;

    const diff = await postgresProvider.diff(
      "id",
      olds,
      await inputs(declare({ project: "/repo/other" })),
    );

    expect(diff.replaces).toContain("project");
  });

  it("replaces when the tier it lands in changed", async () => {
    const olds = (await postgresProvider.create(await inputs(declare()))).outs;

    const diff = await postgresProvider.diff(
      "id",
      olds,
      await inputs(declare({ tier: "preview" })),
    );

    expect(diff.replaces).toContain("tier");
    expect(diff.deleteBeforeReplace).toBe(true);
  });

  it("reports a change rather than throwing while a property is still unknown", async () => {
    const olds = (await postgresProvider.create(await inputs(declare()))).outs;
    postgres("orders", { ...properties, host: undefined as never }, { project: root });

    expect(await postgresProvider.diff("id", olds, await inputs(latest()))).toMatchObject({
      changes: true,
      replaces: [],
    });
  });

  it("reports no change when nothing changed", async () => {
    const olds = (await postgresProvider.create(await inputs(declare()))).outs;

    expect(await postgresProvider.diff("id", olds, await inputs(declare()))).toMatchObject({
      changes: false,
      replaces: [],
    });
  });
});

describe("removing a postgres binding", () => {
  it("runs ocel bindings rm for the name it published, at the version it wrote", async () => {
    const created = await postgresProvider.create(await inputs(declare()));
    run.mockClear();

    await postgresProvider.delete("id", created.outs);

    expect(argv().args).toEqual(["bindings", "rm", "orders", "--at-version", "1", "--json"]);
    expect(argv().options).toMatchObject({ cwd: root });
  });

  it("runs ocel bindings rm in the project under the checkout removing it", async () => {
    vi.spyOn(process, "cwd").mockReturnValue("/laptop/shop");
    postgres("orders", properties);
    const created = await postgresProvider.create(await inputs(latest()));
    run.mockClear();
    vi.spyOn(process, "cwd").mockReturnValue("/ci/work/shop");

    await postgresProvider.delete("id", created.outs);

    expect(argv().options).toMatchObject({ cwd: "/ci/work/shop" });
  });
});

describe("declaring a custom binding", () => {
  const network = {
    subnetIds: ["subnet-0a1", "subnet-0b2"],
    securityGroupIds: ["sg-0c3"],
  };

  const customOwner = "urn:pulumi:production::shop";

  function declareCustom(opts?: Parameters<typeof custom>[2]): Built {
    custom("network", network, { project: root, ...opts });
    return latest();
  }

  it("builds one resource per call, owned by the program it runs in", async () => {
    const one = declareCustom();

    expect(one.name).toMatch(/^ocel-binding-production-network-[0-9a-f]{8}$/);
    expect(await inputs(one)).toMatchObject({
      name: "network",
      tier: "production",
      directory: root,
      owner: customOwner,
      properties: network,
    });
  });

  it("runs ocel bindings set with a custom record sourced to pulumi", async () => {
    const created = await customProvider.create(await inputs(declareCustom()));

    const { args, options } = argv();
    expect(args).toEqual(["bindings", "set", "--owner", customOwner, "--json"]);
    expect(JSON.parse(String(options?.input))).toEqual({
      binding: { name: "network", custom: network, source: "pulumi" },
    });
    expect(created.id).toBe("production/network");
  });

  it("outputs only what the engine can send back, on the production tier", async () => {
    const created = await customProvider.create(await inputs(declareCustom()));

    expect(() => Struct.fromJavaScript(created.outs)).not.toThrow();
  });

  it("reports a change rather than throwing while a property is still unknown", async () => {
    const olds = (await customProvider.create(await inputs(declareCustom()))).outs;
    custom(
      "network",
      { properties: { ...network, subnetIds: undefined as never } },
      { project: root },
    );

    expect(await customProvider.diff("id", olds, await inputs(latest()))).toMatchObject({
      changes: true,
      replaces: [],
    });
  });

  it("reports no change when nothing changed", async () => {
    const olds = (await customProvider.create(await inputs(declareCustom()))).outs;

    expect(await customProvider.diff("id", olds, await inputs(declareCustom()))).toMatchObject({
      changes: false,
      replaces: [],
    });
  });

  it("runs ocel bindings rm for the name it published, at the version it wrote", async () => {
    const created = await customProvider.create(await inputs(declareCustom()));
    run.mockClear();

    await customProvider.delete("id", created.outs);

    expect(argv().args).toEqual(["bindings", "rm", "network", "--at-version", "1", "--json"]);
  });
});

describe("publishing variables", () => {
  const publisher = "urn:pulumi:production::shop";

  beforeEach(() => {
    run.mockReturnValue({
      status: 0,
      stdout: written("API_KEY", "DATABASE_URL"),
      stderr: "",
    } as never);
  });

  function declareVariables(
    values: Parameters<typeof variables>[0] = {
      API_KEY: secret("k3y"),
      DATABASE_URL: output("postgres://db.internal:5432/app"),
    },
    opts?: Parameters<typeof variables>[1],
  ): Built {
    variables(values, { project: root, ...opts });
    return latest();
  }

  it("builds one resource per coordinate, publishing as the program it runs in", async () => {
    const one = declareVariables();

    expect(one.name).toMatch(/^ocel-variables-production-[0-9a-f]{8}$/);
    expect(await inputs(one)).toMatchObject({
      tier: "production",
      directory: root,
      publisher,
      values: { API_KEY: "k3y", DATABASE_URL: "postgres://db.internal:5432/app" },
      secrets: ["API_KEY"],
    });
  });

  it("publishes as the program wherever the call hangs, so moving the call keeps its values its own", async () => {
    const parent = {
      __pulumiResource: true,
      urn: output("urn:pulumi:production::shop::my:component:Api::api"),
    } as unknown as Resource;

    expect((await inputs(declareVariables(undefined, { parent }))).publisher).toBe(publisher);
  });

  it("refuses a second call for one coordinate, which would remove what the first published", () => {
    running.stack = {};
    declareVariables();

    expect(() => declareVariables()).toThrow(/bind.variables is called twice for production/);
  });

  it("names the resource for its coordinate, the same on every run", () => {
    variables({ DATABASE_URL: output("postgres://db.internal:5432/app") });
    variables(
      { DATABASE_URL: output("postgres://db.internal:5432/app") },
      { tier: "preview", environment: "pr-12" },
    );

    expect(built.map((one) => one.name)).toEqual([
      "ocel-variables-production-76bf3358",
      "ocel-variables-preview-pr-12-25e297bc",
    ]);
  });

  it("names the resource for its ocel project, so two projects can each take values", () => {
    running.stack = {};
    declareVariables(undefined, { project: "apps/a" });
    declareVariables(undefined, { project: "apps/b" });

    const [a, b] = built;
    expect(a?.name).not.toBe(b?.name);
  });

  it("keeps its digest secret, since a digest of a secret is a guess at it", () => {
    expect(declareVariables().opts).toMatchObject({ additionalSecretOutputs: ["digest"] });
  });

  it("runs ocel env publish in the project as this resource, marking the secret output", async () => {
    const created = await variablesProvider.create(await inputs(declareVariables()));

    const { command, args, options } = argv();
    expect(command).toBe(ocelCLI);
    expect(args).toEqual(["env", "publish", "--publisher", publisher, "--json"]);
    expect(options).toMatchObject({ cwd: root });
    expect(JSON.parse(String(options?.input))).toEqual({
      values: [
        { key: "API_KEY", value: "k3y", secret: true },
        { key: "DATABASE_URL", value: "postgres://db.internal:5432/app" },
      ],
    });
    expect(created.id).toBe("variables/production");
    expect(created.outs).toMatchObject({ versions: { API_KEY: "1", DATABASE_URL: "1" } });
    expect(JSON.stringify(created.outs)).not.toContain("k3y");
  });

  it("outputs only what the engine can send back, on the production tier", async () => {
    const created = await variablesProvider.create(await inputs(declareVariables()));

    expect(() => Struct.fromJavaScript(created.outs)).not.toThrow();
  });

  it("names the preview environment it publishes into", async () => {
    await variablesProvider.create(
      await inputs(declareVariables(undefined, { tier: "preview", environment: "pr-12" })),
    );

    expect(argv().args.slice(-3)).toEqual(["--preview", "--environment", "pr-12"]);
  });

  it("publishes the whole set again when a value changes, which drops a name it stopped listing", async () => {
    const olds = (await variablesProvider.create(await inputs(declareVariables()))).outs;
    const news = await inputs(declareVariables({ API_KEY: secret("rotated") }));
    run.mockClear();

    expect(await variablesProvider.diff("id", olds, news)).toMatchObject({
      changes: true,
      replaces: [],
    });
    await variablesProvider.update("id", olds, news);

    expect(JSON.parse(String(argv().options?.input))).toEqual({
      values: [{ key: "API_KEY", value: "rotated", secret: true }],
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
        await inputs(declareVariables({ API_KEY: undefined as never })),
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
