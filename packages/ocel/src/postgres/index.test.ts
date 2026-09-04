import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../utils/rpc", () => ({
  rpc: { resource: { declare: vi.fn(() => Promise.resolve({})) } },
}));

const { postgres, connectionStringFor, UnprovisionedResourceError } = await import(
  "./index.js"
);

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

  it("fails cold start naming both types when the record is of another type", () => {
    vi.stubEnv(
      "OCEL_RESOURCE_POSTGRES_orders",
      JSON.stringify({ name: "orders", bucket: { bucket: "orders" } }),
    );

    expect(() => postgres("orders")).toThrow(
      "OCEL_RESOURCE_POSTGRES_orders carries a BUCKET link, and this app reads it as a POSTGRES",
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

  it("refuses every read when this deploy provisioned nothing", () => {
    vi.stubEnv("OCEL_PHASE", "resources-suppressed");

    const pool = postgres("orders");

    expect(() => pool.query).toThrow(
      "'postgres(\"orders\")' cannot be used while resources are suppressed: tried to access 'query', and this deploy provisioned none",
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
