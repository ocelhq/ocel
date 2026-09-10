import { afterEach, describe, expect, it } from "vitest";
import { BindingType } from "../gen/proto/common/bindings/v1/bindings_pb.js";
import { bindingKey, bindingTypeOf, getConfig, getRuntimeAddress } from "./get-config.js";

const LIVE_VALUES = Symbol.for("ocel.env.liveValues");

const postgresRecord = JSON.stringify({
  name: "main",
  postgres: {
    host: "db.internal",
    port: 5433,
    database: "catalog",
    username: "app",
    password: "s3cret",
  },
});

const bucketRecord = JSON.stringify({
  name: "storage",
  bucket: { bucket: "shop-storage" },
});

describe("getConfig", () => {
  const keys: string[] = [];
  afterEach(() => {
    for (const key of keys) delete process.env[key];
    keys.length = 0;
    delete (globalThis as Record<symbol, unknown>)[LIVE_VALUES];
  });

  const setEnv = (key: string, value: string) => {
    keys.push(key);
    process.env[key] = value;
  };

  it("keys a binding by its type's enum name", () => {
    expect(bindingKey("main", BindingType.POSTGRES)).toBe("OCEL_RESOURCE_POSTGRES_main");
    expect(bindingKey("storage", BindingType.BUCKET)).toBe("OCEL_RESOURCE_BUCKET_storage");
  });

  it("reads a POSTGRES binding's typed properties from OCEL_RESOURCE_POSTGRES_<id>", () => {
    setEnv("OCEL_RESOURCE_POSTGRES_main", postgresRecord);

    expect(getConfig("main", "postgres")).toMatchObject({
      host: "db.internal",
      port: 5433,
      database: "catalog",
      username: "app",
      password: "s3cret",
    });
  });

  it("reads a BUCKET binding's typed properties from OCEL_RESOURCE_BUCKET_<id>", () => {
    setEnv("OCEL_RESOURCE_BUCKET_storage", bucketRecord);

    expect(getConfig("storage", "bucket")).toMatchObject({
      bucket: "shop-storage",
    });
  });

  it("prefers a live-delivered value over the process environment", () => {
    setEnv(
      "OCEL_RESOURCE_POSTGRES_main",
      JSON.stringify({ name: "main", postgres: { host: "stale" } }),
    );
    (globalThis as Record<symbol, unknown>)[LIVE_VALUES] = {
      generation: 1,
      values: {
        OCEL_RESOURCE_POSTGRES_main: JSON.stringify({
          name: "main",
          postgres: { host: "live" },
        }),
      },
    };

    expect(getConfig("main", "postgres").host).toBe("live");
  });

  it("refuses a binding of another type, naming both", () => {
    setEnv("OCEL_RESOURCE_POSTGRES_main", bucketRecord);

    expect(() => getConfig("main", "postgres")).toThrow(
      "OCEL_RESOURCE_POSTGRES_main carries a BUCKET binding, and this app reads it as a POSTGRES",
    );
  });

  it("refuses a record that carries no properties", () => {
    setEnv("OCEL_RESOURCE_BUCKET_storage", JSON.stringify({ name: "storage" }));

    expect(() => getConfig("storage", "bucket")).toThrow(
      "carries a UNSPECIFIED binding, and this app reads it as a BUCKET",
    );
  });

  it("refuses a payload that is not a binding record", () => {
    setEnv("OCEL_RESOURCE_BUCKET_storage", "shop-storage");

    expect(() => getConfig("storage", "bucket")).toThrow(
      "OCEL_RESOURCE_BUCKET_storage does not carry a binding record",
    );
  });

  it("throws when the resource env var is undefined", () => {
    expect(() => getConfig("missing", "bucket")).toThrow("OCEL_RESOURCE_BUCKET_missing");
  });
});

describe("bindingTypeOf", () => {
  it("is the type the properties case declares", () => {
    expect(
      bindingTypeOf({
        properties: { case: "postgres", value: {} },
      } as never),
    ).toBe(BindingType.POSTGRES);
    expect(bindingTypeOf({ properties: { case: "bucket", value: {} } } as never)).toBe(
      BindingType.BUCKET,
    );
    expect(bindingTypeOf({ properties: { case: undefined } } as never)).toBe(
      BindingType.UNSPECIFIED,
    );
  });
});

describe("getRuntimeAddress", () => {
  afterEach(() => {
    delete process.env.OCEL_RUNTIME_ADDRESS;
  });

  it("reads the one address every runtime-backed resource shares", () => {
    process.env.OCEL_RUNTIME_ADDRESS = "http://127.0.0.1:41235";

    expect(getRuntimeAddress()).toBe("http://127.0.0.1:41235");
  });

  it("throws when the runtime address is undefined", () => {
    expect(() => getRuntimeAddress()).toThrow("OCEL_RUNTIME_ADDRESS");
  });
});
