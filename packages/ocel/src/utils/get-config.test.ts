import { afterEach, describe, expect, it } from "vitest";
import { getConfig } from "./get-config.js";

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

    expect(getConfig("main", "ocel:postgres")).toBe(
      "postgres://localhost/main",
    );
  });

  it("reads a BUCKET resource from OCEL_RESOURCE_BUCKET_<id>", () => {
    const payload = JSON.stringify({
      address: "http://localhost:4000",
      bucket: "storage",
    });
    setEnv("OCEL_RESOURCE_BUCKET_storage", payload);

    expect(getConfig("storage", "ocel:bucket")).toBe(payload);
  });

  it("derives the env fragment from any ocel-namespaced token", () => {
    setEnv("OCEL_RESOURCE_QUEUE_jobs", "queue-url");

    expect(getConfig("jobs", "ocel:queue")).toBe("queue-url");
  });

  it("throws when the token carries no fragment", () => {
    expect(() => getConfig("main", "ocel:")).toThrow("ocel:");
  });

  it("throws on a foreign-namespaced token", () => {
    expect(() => getConfig("main", "acme:redis")).toThrow("acme:redis");
  });

  it("throws when the resource env var is undefined", () => {
    expect(() => getConfig("missing", "ocel:bucket")).toThrow(
      "OCEL_RESOURCE_BUCKET_missing",
    );
  });
});
