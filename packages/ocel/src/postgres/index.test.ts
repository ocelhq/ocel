import { Client, Pool } from "pg";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ResourceType } from "../gen/proto/app/resources/v1/resources_pb.js";

const declareMock = vi.hoisted(() => vi.fn(() => Promise.resolve({})));

vi.mock("../runtime/rpc", () => ({
  rpc: { resource: { declare: declareMock } },
}));

const { postgres, connectionStringFor, UnprovisionedResourceError } = await import("./index.js");

const LIVE_VALUES = Symbol.for("ocel.env.liveValues");

describe("postgres()", () => {
  beforeEach(() => {
    vi.stubEnv("OCEL_PHASE", "");
  });

  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it("builds the pool from the typed properties, never from a URL", () => {
    vi.stubEnv(
      "OCEL_RESOURCE_POSTGRES_orders",
      JSON.stringify({
        name: "orders",
        postgres: {
          host: "orders.internal",
          port: 6543,
          database: "orders",
          username: "app user",
          password: "p@ss:word/with#odd?chars",
        },
      }),
    );

    const pool = postgres("orders");

    expect(pool.options).toMatchObject({
      host: "orders.internal",
      port: 6543,
      database: "orders",
      user: "app user",
    });
    expect((pool.options.password as () => string)()).toBe("p@ss:word/with#odd?chars");
    expect(pool.connectionString).toBe(
      "postgres://app%20user:p%40ss%3Aword%2Fwith%23odd%3Fchars@orders.internal:6543/orders",
    );
  });

  it("presents the password of the binding's latest generation on each new connection", async () => {
    const live = globalThis as Record<symbol, unknown>;
    const publish = (generation: number, password: string) => {
      live[LIVE_VALUES] = {
        generation,
        values: {
          OCEL_RESOURCE_POSTGRES_orders: JSON.stringify({
            name: "orders",
            postgres: { host: "db", port: 5432, database: "d", username: "u", password },
          }),
        },
      };
    };
    try {
      publish(1, "first");
      const pool = postgres("orders");
      const presented = async () => {
        const { password } = new Client(pool.options) as unknown as {
          password: string | (() => string | Promise<string>);
        };
        return typeof password === "function" ? await password() : password;
      };
      expect(await presented()).toBe("first");

      publish(2, "rotated");

      expect(await presented()).toBe("rotated");
    } finally {
      delete live[LIVE_VALUES];
    }
  });

  it("connects through a record's url verbatim when it has one", () => {
    const url =
      "postgres://app:s3cret@ep-cool.neon.tech/orders?sslmode=require&options=endpoint%3Dep-cool";
    vi.stubEnv(
      "OCEL_RESOURCE_POSTGRES_orders",
      JSON.stringify({ name: "orders", postgres: { url } }),
    );

    const pool = postgres("orders");

    expect(new Client(pool.options)).toMatchObject({
      host: "ep-cool.neon.tech",
      user: "app",
      database: "orders",
    });
    expect(pool.options.connectionString).toContain("options=endpoint%3Dep-cool");
    expect(pool.options).not.toHaveProperty("host");
    expect(pool.connectionString).toBe(url);
  });

  it("keeps a url's sslmode out of the connection string node-postgres reads, negotiating TLS itself", () => {
    const url =
      "postgres://app:s3cret@db.supabase.co/postgres?sslmode=require&application_name=shop";
    vi.stubEnv(
      "OCEL_RESOURCE_POSTGRES_orders",
      JSON.stringify({ name: "orders", postgres: { url } }),
    );

    const pool = postgres("orders");

    expect(pool.options.connectionString).toBe(
      "postgres://app:s3cret@db.supabase.co/postgres?application_name=shop",
    );
    expect(new Client(pool.options).ssl).toBe(false);
    expect(pool.connectionString).toBe(url);
  });

  it("names the record's tls mode as the sslmode of the connection string it exposes", () => {
    vi.stubEnv(
      "OCEL_RESOURCE_POSTGRES_orders",
      JSON.stringify({
        name: "orders",
        postgres: {
          host: "db",
          port: 5432,
          database: "d",
          username: "u",
          password: "p",
          tlsMode: "POSTGRES_TLS_MODE_VERIFY_FULL",
        },
      }),
    );

    expect(new URL(postgres("orders").connectionString).searchParams.get("sslmode")).toBe(
      "verify-full",
    );
  });

  it("declares a database with no binding delivered, and refuses its first use naming why", () => {
    const pool = postgres("orders");

    expect(() => pool.query).toThrow(
      "OCEL_RESOURCE_POSTGRES_orders is not delivered to this process: `ocel dev` delivers it locally and `ocel deploy` to the deployed app, and a build gets it only from an `ocel deploy` whose provider forwards a port to the resource, so code that runs while building, such as prerendering a page, cannot use it under `ocel build`",
    );
    expect(() => pool.connectionString).toThrow("OCEL_RESOURCE_POSTGRES_orders");
  });

  it("resolves as itself when awaited with no binding delivered, since a pool is no thenable", async () => {
    const pool = postgres("orders");

    await expect(Promise.resolve(pool)).resolves.toBe(pool);
  });

  it("is a pg pool whose methods run on the one pool it opens", async () => {
    vi.stubEnv(
      "OCEL_RESOURCE_POSTGRES_orders",
      JSON.stringify({
        name: "orders",
        postgres: { host: "db", port: 5432, database: "d", username: "u", password: "p" },
      }),
    );

    const pool = postgres("orders");

    expect(pool).toBeInstanceOf(Pool);
    expect(pool.totalCount).toBe(0);
    expect(pool.on("error", () => {})).toBe(pool);
    expect(pool.query).toBe(pool.query);
    await pool.end();
    expect(pool.ended).toBe(true);
  });

  it("refuses the first use naming both types when the record is of another type", () => {
    vi.stubEnv(
      "OCEL_RESOURCE_POSTGRES_orders",
      JSON.stringify({ name: "orders", bucket: { bucket: "orders" } }),
    );

    const pool = postgres("orders");

    expect(() => pool.query).toThrow(
      "OCEL_RESOURCE_POSTGRES_orders contains a BUCKET binding, and this app reads it as a POSTGRES",
    );
  });

  it("refuses every read during discovery, naming what was reached for", () => {
    vi.stubEnv("OCEL_PHASE", "discovery");

    const pool = postgres("orders");

    expect(() => pool.query).toThrow(
      "'postgres(\"orders\")' cannot be used during discovery: tried to access 'query' before the resource was provisioned",
    );
    expect(() => pool.query).toThrow(UnprovisionedResourceError);
  });

  it("percent-encodes credentials in the connection string it exposes", () => {
    const url = new URL(connectionStringFor("h", 5432, "d", "u:s", "p/w"));

    expect(decodeURIComponent(url.username)).toBe("u:s");
    expect(decodeURIComponent(url.password)).toBe("p/w");
    expect(url.pathname).toBe("/d");
    expect(url.port).toBe("5432");
  });
});

describe("postgres() during discovery", () => {
  beforeEach(() => {
    declareMock.mockClear();
  });

  it("declares a POSTGRES resource of version 17 unless told otherwise", () => {
    postgres("main");
    postgres("legacy", { version: "16" });

    expect(declareMock).toHaveBeenCalledWith(
      expect.objectContaining({
        resource: { name: "main", type: ResourceType.POSTGRES },
        config: { case: "postgres", value: { version: "17" } },
      }),
    );
    expect(declareMock).toHaveBeenCalledWith(
      expect.objectContaining({
        resource: { name: "legacy", type: ResourceType.POSTGRES },
        config: { case: "postgres", value: { version: "16" } },
      }),
    );
  });
});
