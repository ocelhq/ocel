import { env } from "cloudflare:test";
import { describe, expect, it } from "vitest";

import { readEntry, writeEntry } from "../src/entry";

const PREFIX = "prod/proj/web/BUILD1";

describe("writeEntry", () => {
  it("puts the body at the object key as JSON", async () => {
    const key = `${PREFIX}/cache/written.cache.json`;
    expect(await writeEntry(env.OCEL_CACHE_STORE, key, `{"value":1}`)).toBe("written");

    const stored = await env.OCEL_CACHE_STORE.get(key);
    expect(await stored?.text()).toBe(`{"value":1}`);
    expect(stored?.httpMetadata?.contentType).toBe("application/json");
  });

  it("reports a rate-limited write instead of retrying it", async () => {
    const bucket = {
      put: async () => {
        throw Object.assign(new Error("put: Too Many Requests (10047)"), { code: 10047 });
      },
    } as unknown as R2Bucket;

    expect(await writeEntry(bucket, "k", "{}")).toBe("rate-limited");
  });

  it("propagates any other failure", async () => {
    const bucket = {
      put: async () => {
        throw new Error("put: Internal Error (10001)");
      },
    } as unknown as R2Bucket;

    await expect(writeEntry(bucket, "k", "{}")).rejects.toThrow("Internal Error");
  });
});

describe("readEntry", () => {
  it("returns the body stored at the object key", async () => {
    const key = `${PREFIX}/cache/read.cache.json`;
    await writeEntry(env.OCEL_CACHE_STORE, key, `{"value":2}`);

    expect(await readEntry(env.OCEL_CACHE_STORE, key)).toBe(`{"value":2}`);
  });

  it("reports an absent object as a miss rather than an error", async () => {
    expect(await readEntry(env.OCEL_CACHE_STORE, `${PREFIX}/cache/never.cache.json`)).toBeNull();
  });
});
