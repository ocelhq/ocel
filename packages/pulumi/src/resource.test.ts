import { output, type Resource } from "@pulumi/pulumi";
import { spawnSync } from "node:child_process";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  custom,
  customProvider,
  postgres,
  postgresProvider,
} from "./resource.js";

interface Built {
  name: string;
  props: Record<string, unknown>;
  opts: Record<string, unknown> | undefined;
}

const { built, FakeResource } = vi.hoisted(() => {
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
  return { built, FakeResource };
});

vi.mock("@pulumi/pulumi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@pulumi/pulumi")>();
  return { ...actual, dynamic: { ...actual.dynamic, Resource: FakeResource } };
});

vi.mock("node:child_process", () => ({ spawnSync: vi.fn() }));

const manifest = "/repo/app/node_modules/ocel/package.json";

vi.mock("node:module", () => ({
  createRequire: () => ({ resolve: () => manifest }),
}));

const run = vi.mocked(spawnSync);

const entry = join("/repo/app/node_modules/ocel", "bin", "run.js");

const root = "/repo/app";

const ownerUrn =
  "urn:pulumi:production::shop::pulumi-nodejs:dynamic:Resource::ocel-link-orders";

const properties = {
  host: "orders.cluster-c.us-east-1.rds.amazonaws.com",
  port: 5432,
  database: "orders",
  username: "operator",
  password: "hunter2",
};

beforeEach(() => {
  built.length = 0;
  run.mockReturnValue({ status: 0, stderr: "" } as never);
});

afterEach(() => {
  vi.clearAllMocks();
});

function settled(value: unknown): Promise<unknown> {
  return new Promise((resolve) => {
    output(value).apply(resolve);
  });
}

async function inputs(one: Built) {
  const resolved: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(one.props)) {
    resolved[key] = await settled(value);
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

function argv() {
  const call = run.mock.calls[0];
  if (!call) throw new Error("the ocel CLI was never run");
  return { command: call[0], args: call[1] as string[], options: call[2] };
}

describe("declaring a postgres link", () => {
  it("builds one resource per call, owned by that resource's own urn", async () => {
    const one = declare();

    expect(one.name).toBe("ocel-link-orders");
    expect(await inputs(one)).toMatchObject({
      name: "orders",
      class: "production",
      project: root,
      owner: ownerUrn,
      properties,
    });
  });

  it("takes the ocel project from the directory pulumi runs in unless it is given", () => {
    postgres("orders", properties);

    expect(latest().props.project).toBe(process.cwd());
  });

  it("owns the link by the urn it has under the parent it hangs from", async () => {
    const parent = {
      __pulumiResource: true,
      urn: output("urn:pulumi:production::shop::aws:rds/instance:Instance::db"),
    } as unknown as Resource;

    const one = declare({ parent });

    expect(one.opts).toMatchObject({ parent });
    expect((await inputs(one)).owner).toBe(
      "urn:pulumi:production::shop::aws:rds/instance:Instance$pulumi-nodejs:dynamic:Resource::ocel-link-orders",
    );
  });

  it("refuses an environment outside the preview class", () => {
    expect(() => declare({ environment: "pr-12" })).toThrow(
      /is named alongside class production/,
    );
  });

  it("refuses the reserved class-wide marker", () => {
    expect(() => declare({ class: "preview", environment: "*" })).toThrow(
      /reserved/,
    );
  });

  it("keeps only the fields a postgres link carries", async () => {
    postgres("orders", { ...properties, extra: "dropped" } as never, {
      project: root,
    });

    expect((await inputs(latest())).properties).toEqual(properties);
  });
});

describe("publishing a postgres link", () => {
  it("runs ocel link set in the project, owned by this resource", async () => {
    const created = await postgresProvider.create(
      await inputs(declare()),
    );

    const { command, args, options } = argv();
    expect(command).toBe(process.execPath);
    expect(args).toEqual([entry, "link", "set", "--owner", ownerUrn]);
    expect(options).toMatchObject({ cwd: root });
    expect(JSON.parse(String(options?.input))).toEqual({
      name: "orders",
      postgres: properties,
      source: "pulumi",
    });
    expect(created.id).toBe("production/orders");
  });

  it("names the preview environment it publishes into", async () => {
    await postgresProvider.create(
      await inputs(declare({ class: "preview", environment: "pr-12" })),
    );

    expect(argv().args.slice(-3)).toEqual([
      "--preview",
      "--environment",
      "pr-12",
    ]);
  });

  it("binds class-wide when no preview environment is named", async () => {
    await postgresProvider.create(
      await inputs(declare({ class: "preview" })),
    );

    expect(argv().args.slice(-1)).toEqual(["--preview"]);
  });

  it("holds a digest and never a property", async () => {
    const created = await postgresProvider.create(
      await inputs(declare()),
    );

    expect(created.outs.digest).toMatch(/^[0-9a-f]{64}$/);
    expect(JSON.stringify(created.outs)).not.toContain(properties.password);
  });

  it("resolves a grant's resource from a pulumi output before it publishes", async () => {
    const resourceArn =
      "arn:aws:rds-db:us-east-1:111122223333:dbuser:cluster-abc/operator";
    postgres(
      "orders",
      {
        ...properties,
        grants: [
          { actions: ["rds-db:connect"], resources: [output(resourceArn)] },
        ],
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
      stderr:
        "link orders in production is already published by publisher urn:pulumi:other\n",
    } as never);

    await expect(
      postgresProvider.create(await inputs(declare())),
    ).rejects.toThrow(
      "link orders in production is already published by publisher urn:pulumi:other",
    );
  });
});

describe("changing a published postgres link", () => {
  it("updates in place when only the record changed", async () => {
    const olds = (await postgresProvider.create(await inputs(declare()))).outs;
    postgres("orders", { ...properties, host: "moved" }, { project: root });

    const diff = await postgresProvider.diff(
      "id",
      olds,
      await inputs(latest()),
    );

    expect(diff).toMatchObject({ changes: true, replaces: [] });
  });

  it("replaces when the class it lands in changed", async () => {
    const olds = (await postgresProvider.create(await inputs(declare()))).outs;

    const diff = await postgresProvider.diff(
      "id",
      olds,
      await inputs(declare({ class: "preview" })),
    );

    expect(diff.replaces).toContain("class");
    expect(diff.deleteBeforeReplace).toBe(true);
  });

  it("reports a change rather than throwing while a property is still unknown", async () => {
    const olds = (await postgresProvider.create(await inputs(declare()))).outs;
    postgres(
      "orders",
      { ...properties, host: undefined as never },
      { project: root },
    );

    expect(
      await postgresProvider.diff("id", olds, await inputs(latest())),
    ).toMatchObject({ changes: true, replaces: [] });
  });

  it("holds still when nothing changed", async () => {
    const olds = (await postgresProvider.create(await inputs(declare()))).outs;

    expect(
      await postgresProvider.diff("id", olds, await inputs(declare())),
    ).toMatchObject({ changes: false, replaces: [] });
  });
});

describe("removing a postgres link", () => {
  it("runs ocel link rm for the name it published", async () => {
    const created = await postgresProvider.create(
      await inputs(declare()),
    );
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

  const customOwner =
    "urn:pulumi:production::shop::pulumi-nodejs:dynamic:Resource::ocel-link-network";

  function declareCustom(opts?: Parameters<typeof custom>[2]): Built {
    custom("network", { properties: network }, { project: root, ...opts });
    return latest();
  }

  it("builds one resource per call, owned by that resource's own urn", async () => {
    const one = declareCustom();

    expect(one.name).toBe("ocel-link-network");
    expect(await inputs(one)).toMatchObject({
      name: "network",
      class: "production",
      project: root,
      owner: customOwner,
      properties: network,
    });
  });

  it("runs ocel link set with a custom record sourced to pulumi", async () => {
    const created = await customProvider.create(
      await inputs(declareCustom()),
    );

    const { args, options } = argv();
    expect(args).toEqual([entry, "link", "set", "--owner", customOwner]);
    expect(JSON.parse(String(options?.input))).toEqual({
      name: "network",
      custom: network,
      source: "pulumi",
    });
    expect(created.id).toBe("production/network");
  });

  it("reports a change rather than throwing while a property is still unknown", async () => {
    const olds = (await customProvider.create(await inputs(declareCustom()))).outs;
    custom(
      "network",
      { properties: { ...network, subnetIds: undefined as never } },
      { project: root },
    );

    expect(
      await customProvider.diff("id", olds, await inputs(latest())),
    ).toMatchObject({ changes: true, replaces: [] });
  });

  it("holds still when nothing changed", async () => {
    const olds = (await customProvider.create(await inputs(declareCustom()))).outs;

    expect(
      await customProvider.diff(
        "id",
        olds,
        await inputs(declareCustom()),
      ),
    ).toMatchObject({ changes: false, replaces: [] });
  });

  it("runs ocel link rm for the name it published", async () => {
    const created = await customProvider.create(
      await inputs(declareCustom()),
    );
    run.mockClear();

    await customProvider.delete("id", created.outs);

    expect(argv().args).toEqual([entry, "link", "rm", "network"]);
  });
});
