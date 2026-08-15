import { afterEach, describe, expect, it, vi } from "vitest";
import { provider, publish } from "./index.js";
import type { PublisherInputs } from "./publisher.js";

vi.mock("./hop.js", () => ({
  hop: () => ({ published: ["orders"], pruned: 0 }),
}));

const args: PublisherInputs = {
  project: "acme",
  class: "production",
  links: {
    orders: {
      urn: "urn:pulumi:production::acme::sst:aws:Postgres::Orders",
      getSSTLink: () => ({
        properties: { host: "h", port: 5432, password: "hunter2" },
      }),
    },
  },
};

const resolved: PublisherInputs = {
  project: "acme",
  class: "production",
  instance: "Ocel",
  links: {
    orders: {
      urn: "urn:pulumi:production::acme::sst:aws:Postgres::Orders",
      properties: { host: "h", port: 5432, password: "hunter2" },
    },
  },
};

afterEach(() => {
  delete (globalThis as { $util?: unknown }).$util;
});

function record(): Record<string, unknown> | undefined {
  let seen: Record<string, unknown> | undefined;
  (globalThis as { $util?: unknown }).$util = {
    dynamic: {
      Resource: class {
        constructor(
          _provider: unknown,
          _name: string,
          props: Record<string, unknown>,
        ) {
          seen = props;
        }
      },
    },
  };
  publish("Ocel", args);
  return seen;
}

describe("publish", () => {
  it("refuses to run outside sst.config.ts rather than half-publishing", () => {
    expect(() => publish("Ocel", args)).toThrow(/sst\.config\.ts/);
  });

  it("hands pulumi the resolvable shape of every link it was given", () => {
    const seen = record();

    expect(seen?.project).toBe("acme");
    expect(seen?.class).toBe("production");
    expect(seen?.links).toEqual({
      orders: {
        urn: "urn:pulumi:production::acme::sst:aws:Postgres::Orders",
        properties: { host: "h", port: 5432, password: "hunter2" },
      },
    });
  });

  it("names the instance after the resource, so a second publisher is a second owner", () => {
    expect(record()?.instance).toBe("Ocel");
  });
});

describe("provider state", () => {
  it("keeps a link's properties out of the state file", async () => {
    const outs = await provider.update("id", resolved, resolved);

    expect(JSON.stringify(outs)).not.toContain("hunter2");
    expect(outs.outs).not.toHaveProperty("links");
    expect(outs.outs.project).toBe("acme");
  });

  it("reports no change when the records it published are the same ones", async () => {
    const { outs } = await provider.update("id", resolved, resolved);

    expect(await provider.diff("id", outs, resolved)).toMatchObject({
      changes: false,
      replaces: [],
    });
  });

  it("reports a change when a link's properties move", async () => {
    const { outs } = await provider.update("id", resolved, resolved);
    const moved = {
      ...resolved,
      links: {
        orders: {
          urn: "urn:pulumi:production::acme::sst:aws:Postgres::Orders",
          properties: { host: "moved", port: 5432, password: "hunter2" },
        },
      },
    };

    expect(await provider.diff("id", outs, moved)).toMatchObject({
      changes: true,
    });
  });
});
