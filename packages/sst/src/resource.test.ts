import { spawnSync } from "node:child_process";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { custom, customProvider, postgres, postgresProvider } from "./resource.js";

vi.mock("node:child_process", () => ({ spawnSync: vi.fn() }));

const manifest = "/repo/app/node_modules/ocel/package.json";

vi.mock("node:module", () => ({
  createRequire: () => ({ resolve: () => manifest }),
}));

const run = vi.mocked(spawnSync);

const entry = join("/repo/app/node_modules/ocel", "bin", "run.js");

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
}

const built: Built[] = [];

class FakeResource {
  constructor(_provider: unknown, name: string, props: Record<string, unknown>) {
    built.push({ name, props });
  }
}

beforeEach(() => {
  built.length = 0;
  run.mockReturnValue({ status: 0, stderr: "" } as never);
  Object.assign(globalThis, {
    $util: {
      getStack: () => "production",
      getProject: () => "shop",
      dynamic: { Resource: FakeResource },
    },
    $cli: { paths: { root } },
  });
});

afterEach(() => {
  vi.clearAllMocks();
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

function argv() {
  const call = run.mock.calls[0];
  if (!call) throw new Error("the ocel CLI was never run");
  return { command: call[0], args: call[1] as string[], options: call[2] };
}

describe("declaring a postgres link", () => {
  it("builds one resource per call, owned by that resource alone", () => {
    const one = declare();

    expect(one.name).toBe("ocel-link-orders");
    expect(one.props).toMatchObject({
      name: "orders",
      class: "production",
      project: root,
      owner:
        "urn:pulumi:production::shop::pulumi:pulumi:Stack$pulumi-nodejs:dynamic:Resource::ocel-link-orders",
      properties,
    });
  });

  it("takes the ocel project from the sst config root unless it is given", () => {
    expect(declare({ project: "/repo/other" }).props.project).toBe(
      "/repo/other",
    );
  });

  it("refuses an environment outside the preview class", () => {
    expect(() => declare({ environment: "pr-12" })).toThrow(
      /is named alongside class production/,
    );
  });

  it("refuses the reserved class-wide marker", () => {
    expect(() =>
      declare({ class: "preview", environment: "*" }),
    ).toThrow(/reserved/);
  });

  it("keeps only the fields a postgres link carries", () => {
    postgres("orders", {
      getSSTLink: () => ({ properties: { ...properties, extra: "dropped" } }),
    });

    expect(built[0]?.props.properties).toEqual(properties);
  });
});

describe("publishing a postgres link", () => {
  it("runs ocel link set in the project, owned by this resource", async () => {
    const created = await postgresProvider.create(declare().props as never);

    const { command, args, options } = argv();
    expect(command).toBe(process.execPath);
    expect(args).toEqual([
      entry,
      "link",
      "set",
      "--owner",
      "urn:pulumi:production::shop::pulumi:pulumi:Stack$pulumi-nodejs:dynamic:Resource::ocel-link-orders",
    ]);
    expect(options).toMatchObject({ cwd: root });
    expect(JSON.parse(String(options?.input))).toEqual({
      name: "orders",
      postgres: properties,
      source: "sst",
    });
    expect(created.id).toBe("production/orders");
  });

  it("names the preview environment it publishes into", async () => {
    await postgresProvider.create(
      declare({ class: "preview", environment: "pr-12" }).props as never,
    );

    expect(argv().args.slice(-3)).toEqual([
      "--preview",
      "--environment",
      "pr-12",
    ]);
  });

  it("binds class-wide when no preview environment is named", async () => {
    await postgresProvider.create(declare({ class: "preview" }).props as never);

    expect(argv().args.slice(-1)).toEqual(["--preview"]);
  });

  it("holds a digest and never a property", async () => {
    const created = await postgresProvider.create(declare().props as never);

    expect(created.outs.digest).toMatch(/^[0-9a-f]{64}$/);
    expect(JSON.stringify(created.outs)).not.toContain(properties.password);
  });

  it("surfaces the CLI's refusal verbatim", async () => {
    run.mockReturnValue({
      status: 1,
      stderr:
        "link orders in production is already published by publisher urn:pulumi:other\n",
    } as never);

    await expect(
      postgresProvider.create(declare().props as never),
    ).rejects.toThrow(
      "link orders in production is already published by publisher urn:pulumi:other",
    );
  });
});

describe("changing a published postgres link", () => {
  it("updates in place when only the record changed", async () => {
    const olds = (await postgresProvider.create(declare().props as never)).outs;
    postgres("orders", {
      getSSTLink: () => ({ properties: { ...properties, host: "moved" } }),
    });

    const diff = await postgresProvider.diff(
      "id",
      olds,
      latest().props as never,
    );

    expect(diff).toMatchObject({ changes: true, replaces: [] });
  });

  it("replaces when the class it lands in changed", async () => {
    const olds = (await postgresProvider.create(declare().props as never)).outs;

    const diff = await postgresProvider.diff(
      "id",
      olds,
      declare({ class: "preview" }).props as never,
    );

    expect(diff.replaces).toContain("class");
    expect(diff.deleteBeforeReplace).toBe(true);
  });

  it("reports a change rather than throwing while a property is still unknown", async () => {
    const olds = (await postgresProvider.create(declare().props as never)).outs;
    postgres("orders", {
      getSSTLink: () => ({
        properties: { ...properties, host: undefined },
      }),
    });

    expect(
      await postgresProvider.diff("id", olds, latest().props as never),
    ).toMatchObject({ changes: true, replaces: [] });
  });

  it("holds still when nothing changed", async () => {
    const olds = (await postgresProvider.create(declare().props as never)).outs;

    expect(
      await postgresProvider.diff("id", olds, declare().props as never),
    ).toMatchObject({ changes: false, replaces: [] });
  });
});

describe("removing a postgres link", () => {
  it("runs ocel link rm for the name it published", async () => {
    const created = await postgresProvider.create(declare().props as never);
    run.mockClear();

    await postgresProvider.delete("id", created.outs);

    expect(argv().args).toEqual([entry, "link", "rm", "orders"]);
    expect(argv().options).toMatchObject({ cwd: root });
  });
});

describe("declaring a custom link", () => {
  const network = {
    subnetIds: ["subnet-0a1", "subnet-0b2"],
    securityGroupIds: ["sg-0c3"],
  };

  function declareCustom(opts?: Parameters<typeof custom>[2]) {
    custom("network", { properties: network }, opts);
    return latest();
  }

  it("builds one resource per call, owned by that resource alone", () => {
    const one = declareCustom();

    expect(one.name).toBe("ocel-link-network");
    expect(one.props).toMatchObject({
      name: "network",
      class: "production",
      project: root,
      owner:
        "urn:pulumi:production::shop::pulumi:pulumi:Stack$pulumi-nodejs:dynamic:Resource::ocel-link-network",
      properties: network,
    });
  });

  it("runs ocel link set with a custom record sourced to sst", async () => {
    const created = await customProvider.create(declareCustom().props as never);

    const { args, options } = argv();
    expect(args).toEqual([
      entry,
      "link",
      "set",
      "--owner",
      "urn:pulumi:production::shop::pulumi:pulumi:Stack$pulumi-nodejs:dynamic:Resource::ocel-link-network",
    ]);
    expect(JSON.parse(String(options?.input))).toEqual({
      name: "network",
      custom: network,
      source: "sst",
    });
    expect(created.id).toBe("production/network");
  });

  it("reports a change rather than throwing while a property is still unknown", async () => {
    const olds = (await customProvider.create(declareCustom().props as never))
      .outs;
    custom("network", { properties: { ...network, subnetIds: undefined } });

    expect(
      await customProvider.diff("id", olds, latest().props as never),
    ).toMatchObject({ changes: true, replaces: [] });
  });

  it("holds still when nothing changed", async () => {
    const olds = (await customProvider.create(declareCustom().props as never))
      .outs;

    expect(
      await customProvider.diff("id", olds, declareCustom().props as never),
    ).toMatchObject({ changes: false, replaces: [] });
  });

  it("runs ocel link rm for the name it published", async () => {
    const created = await customProvider.create(declareCustom().props as never);
    run.mockClear();

    await customProvider.delete("id", created.outs);

    expect(argv().args).toEqual([entry, "link", "rm", "network"]);
  });
});
