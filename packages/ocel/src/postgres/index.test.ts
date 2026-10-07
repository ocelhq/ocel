import { Client, Pool } from "pg";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../runtime/rpc", () => ({
  rpc: { resource: { declare: vi.fn(() => Promise.resolve({})) } },
}));

const { postgres, connectionStringFor, UnprovisionedResourceError } = await import("./index.js");

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
      password: "p@ss:word/with#odd?chars",
    });
    expect(pool.connectionString).toBe(
      "postgres://app%20user:p%40ss%3Aword%2Fwith%23odd%3Fchars@orders.internal:6543/orders",
    );
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

  it("reads a url's sslmode as libpq does, so require encrypts without checking the certificate", () => {
    const url = "postgres://app:s3cret@db.supabase.co/postgres?sslmode=require";
    vi.stubEnv(
      "OCEL_RESOURCE_POSTGRES_orders",
      JSON.stringify({ name: "orders", postgres: { url } }),
    );

    const pool = postgres("orders");

    expect(new Client(pool.options).ssl).toMatchObject({ rejectUnauthorized: false });
    expect(pool.connectionString).toBe(url);
  });

  it("verifies the server under a url's sslmode=verify-full", () => {
    vi.stubEnv(
      "OCEL_RESOURCE_POSTGRES_orders",
      JSON.stringify({
        name: "orders",
        postgres: { url: "postgres://app:s3cret@db/postgres?sslmode=verify-full" },
      }),
    );

    const ssl = new Client(postgres("orders").options).ssl;

    expect(ssl).not.toMatchObject({ rejectUnauthorized: false });
    expect(ssl).toBeTruthy();
  });

  it("encrypts the connection without checking the certificate when the record requires tls", () => {
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
          tlsMode: "POSTGRES_TLS_MODE_REQUIRE",
        },
      }),
    );

    const pool = postgres("orders");

    expect(pool.options).toMatchObject({ ssl: { rejectUnauthorized: false } });
    expect(new URL(pool.connectionString).searchParams.get("sslmode")).toBe("require");
  });

  it("verifies the server against the record's CA under verify-full", () => {
    const ca = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n";
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
          tlsCa: ca,
        },
      }),
    );

    const pool = postgres("orders");

    expect(pool.options).toMatchObject({ ssl: { rejectUnauthorized: true, ca } });
    expect(new URL(pool.connectionString).searchParams.get("sslmode")).toBe("verify-full");
  });

  it("verifies the host a forwarded record names as its tls server name, not the forward it connects to", () => {
    vi.stubEnv(
      "OCEL_RESOURCE_POSTGRES_orders",
      JSON.stringify({
        name: "orders",
        postgres: {
          host: "127.0.0.1",
          port: 41234,
          database: "d",
          username: "u",
          password: "p",
          tlsMode: "POSTGRES_TLS_MODE_VERIFY_FULL",
          tlsServerName: "orders.cluster.internal",
        },
      }),
    );

    const pool = postgres("orders");

    expect(pool.options).toMatchObject({
      host: "127.0.0.1",
      port: 41234,
      ssl: { rejectUnauthorized: true, servername: "orders.cluster.internal" },
    });
  });

  it("leaves tls to the driver when the record names no mode", () => {
    vi.stubEnv(
      "OCEL_RESOURCE_POSTGRES_orders",
      JSON.stringify({
        name: "orders",
        postgres: { host: "db", port: 5432, database: "d", username: "u", password: "p" },
      }),
    );

    expect(postgres("orders").options).not.toHaveProperty("ssl");
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
