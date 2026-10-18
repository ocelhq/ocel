import { spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { custom, customProvider, postgres, postgresProvider } from "./resource.js";

vi.mock("node:child_process", () => {
  const spawnSync = vi.fn();
  return { spawnSync, default: { spawnSync } };
});

const manifest = "/repo/app/node_modules/ocel/package.json";

vi.mock("node:module", () => {
  const createRequire = () => ({ resolve: () => manifest });
  return { createRequire, default: { createRequire } };
});

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
  vi.restoreAllMocks();
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
    expect(command).toBe(process.execPath);
    expect(args).toEqual([
      entry,
      "bindings",
      "set",
      "--owner",
      "urn:pulumi:production::shop::pulumi:pulumi:Stack$pulumi-nodejs:dynamic:Resource::ocel-binding-orders",
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

    expect(argv().args).toEqual([entry, "bindings", "rm", "orders"]);
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
      entry,
      "bindings",
      "set",
      "--owner",
      "urn:pulumi:production::shop::pulumi:pulumi:Stack$pulumi-nodejs:dynamic:Resource::ocel-binding-network",
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

    expect(argv().args).toEqual([entry, "bindings", "rm", "network"]);
  });
});
