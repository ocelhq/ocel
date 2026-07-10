import { afterEach, describe, expect, it } from "vitest";
import { getConfig } from "./get-config";
import { ResourceType } from "./rpc";

describe("getConfig", () => {
  const keys: string[] = [];
  afterEach(() => {
    for (const key of keys) delete process.env[key];
    keys.length = 0;
  });

  const setEnv = (key: string, value: string) => {
    keys.push(key);
    process.env[key] = value;
  };

  it("reads a POSTGRES resource from OCEL_RESOURCE_POSTGRES_<id>", () => {
    setEnv("OCEL_RESOURCE_POSTGRES_main", "postgres://localhost/main");

    expect(getConfig("main", ResourceType.POSTGRES)).toBe(
      "postgres://localhost/main",
    );
  });

  it("reads a BUCKET resource from OCEL_RESOURCE_BUCKET_<id>", () => {
    const payload = JSON.stringify({
      address: "http://localhost:4000",
      bucket: "storage",
    });
    setEnv("OCEL_RESOURCE_BUCKET_storage", payload);

    expect(getConfig("storage", ResourceType.BUCKET)).toBe(payload);
  });

  it("throws when the resource env var is undefined", () => {
    expect(() => getConfig("missing", ResourceType.BUCKET)).toThrow(
      "OCEL_RESOURCE_BUCKET_missing",
    );
  });
});
