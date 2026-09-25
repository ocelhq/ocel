import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../utils/rpc", () => ({
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

  it("connects through a record's url verbatim when it carries one", () => {
    const url =
      "postgres://app:s3cret@ep-cool.neon.tech/orders?sslmode=require&options=endpoint%3Dep-cool";
    vi.stubEnv(
      "OCEL_RESOURCE_POSTGRES_orders",
      JSON.stringify({ name: "orders", postgres: { url } }),
    );

    const pool = postgres("orders");

    expect(pool.options).toMatchObject({ connectionString: url });
    expect(pool.options).not.toHaveProperty("host");
    expect(pool.connectionString).toBe(url);
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
          tlsMode: "require",
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
          tlsMode: "verify-full",
          tlsCa: ca,
        },
      }),
    );

    const pool = postgres("orders");

    expect(pool.options).toMatchObject({ ssl: { rejectUnauthorized: true, ca } });
    expect(new URL(pool.connectionString).searchParams.get("sslmode")).toBe("verify-full");
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

  it("fails cold start naming both types when the record is of another type", () => {
    vi.stubEnv(
      "OCEL_RESOURCE_POSTGRES_orders",
      JSON.stringify({ name: "orders", bucket: { bucket: "orders" } }),
    );

    expect(() => postgres("orders")).toThrow(
      "OCEL_RESOURCE_POSTGRES_orders carries a BUCKET binding, and this app reads it as a POSTGRES",
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
