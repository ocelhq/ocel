import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { LinkError, link } from "./index.js";

const LIVE_VALUES = Symbol.for("ocel.env.liveValues");

function publish(values: Record<string, string>, generation = 1) {
  (globalThis as Record<symbol, unknown>)[LIVE_VALUES] = {
    generation,
    values,
  };
}

function unpublish() {
  delete (globalThis as Record<symbol, unknown>)[LIVE_VALUES];
}

const postgresRecord = JSON.stringify({
  type: "ocel:postgres",
  properties: {
    host: "db.internal",
    port: "5432",
    database: "app",
    username: "app",
    password: "hunter2",
  },
});

describe("a typed accessor at runtime", () => {
  beforeEach(() => {
    vi.stubEnv("OCEL_PHASE", "");
  });

  afterEach(() => {
    vi.unstubAllEnvs();
    unpublish();
  });

  it("resolves an ocel-owned token from the process environment", () => {
    vi.stubEnv("OCEL_LINK_main", postgresRecord);

    expect(link.postgres("main")).toEqual({
      host: "db.internal",
      port: "5432",
      database: "app",
      username: "app",
      password: "hunter2",
    });
  });

  it("resolves a bucket record", () => {
    vi.stubEnv("OCEL_LINK_uploads", JSON.stringify({
      type: "ocel:bucket",
      properties: { bucket: "acme-uploads" },
    }));

    expect(link.bucket("uploads")).toEqual({ bucket: "acme-uploads" });
  });

  it("prefers a live value over the baked one", () => {
    vi.stubEnv("OCEL_LINK_main", postgresRecord);
    publish({
      OCEL_LINK_main: JSON.stringify({
        type: "ocel:postgres",
        properties: {
          host: "rotated.internal",
          port: "5432",
          database: "app",
          username: "app",
          password: "rotated",
        },
      }),
    });

    expect(link.postgres("main").password).toBe("rotated");
  });

  it("names the link and the binding when nothing delivered it", () => {
    expect(() => link.postgres("main")).toThrow(LinkError);
    expect(() => link.postgres("main")).toThrow(/'main'/);
    expect(() => link.postgres("main")).toThrow(/links/);
  });

  it("rejects a record whose token is not the one asked for", () => {
    vi.stubEnv("OCEL_LINK_main", JSON.stringify({
      type: "sst:aws.Postgres",
      properties: { host: "db.internal" },
    }));

    expect(() => link.postgres("main")).toThrow(
      /'ocel:postgres'.*'sst:aws\.Postgres'/,
    );
  });

  it("rejects a record missing a property the token's shape requires", () => {
    vi.stubEnv("OCEL_LINK_main", JSON.stringify({
      type: "ocel:postgres",
      properties: { host: "db.internal", port: "5432" },
    }));

    expect(() => link.postgres("main")).toThrow(/database/);
  });

  it("rejects a payload that is not a link record", () => {
    vi.stubEnv("OCEL_LINK_main", "not json");

    expect(() => link.postgres("main")).toThrow(LinkError);
  });
});

describe("the raw escape hatch", () => {
  beforeEach(() => {
    vi.stubEnv("OCEL_PHASE", "");
  });

  afterEach(() => {
    vi.unstubAllEnvs();
    unpublish();
  });

  it("hands back the raw properties of a foreign token unvalidated", () => {
    vi.stubEnv("OCEL_LINK_kafka", JSON.stringify({
      type: "acme:kafka",
      properties: { brokers: "a:9092,b:9092", topic: "orders" },
    }));

    expect(link.custom("kafka", "acme:kafka")).toEqual({
      brokers: "a:9092,b:9092",
      topic: "orders",
    });
  });

  it("still refuses a record whose token is not the one asked for", () => {
    vi.stubEnv("OCEL_LINK_kafka", JSON.stringify({
      type: "acme:redpanda",
      properties: {},
    }));

    expect(() => link.custom("kafka", "acme:kafka")).toThrow(
      /'acme:kafka'.*'acme:redpanda'/,
    );
  });

  it("validates an ocel-owned token asked for through custom", () => {
    vi.stubEnv("OCEL_LINK_main", JSON.stringify({
      type: "ocel:postgres",
      properties: { host: "db.internal" },
    }));

    expect(() => link.custom("main", "ocel:postgres")).toThrow(LinkError);
  });
});

describe("the discovery phase", () => {
  afterEach(() => {
    vi.unstubAllEnvs();
    unpublish();
  });

  it("hands back a handle that resolves nothing", () => {
    vi.stubEnv("OCEL_PHASE", "discovery");

    expect(() => link.postgres("main")).not.toThrow();
  });

  it("refuses a property read, naming the link", () => {
    vi.stubEnv("OCEL_PHASE", "discovery");

    expect(() => link.postgres("main").host).toThrow(/'main'.*discovery/s);
  });

  it("refuses a property read through the escape hatch too", () => {
    vi.stubEnv("OCEL_PHASE", "discovery");

    expect(() => link.custom("kafka", "acme:kafka").brokers).toThrow(
      /'kafka'.*discovery/s,
    );
  });

  it("ignores a delivered value while declaring", () => {
    vi.stubEnv("OCEL_PHASE", "discovery");
    vi.stubEnv("OCEL_LINK_main", postgresRecord);

    expect(() => link.postgres("main").host).toThrow(LinkError);
  });
});
