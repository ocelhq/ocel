import { env } from "cloudflare:test";
import { describe, expect, it } from "vitest";

import { entryObjectKey, writeEntry } from "../src/write";

const PREFIX = "prod/proj/web/BUILD1";

describe("entryObjectKey", () => {
  it("roots the key under the deploy's own cache slice", () => {
    expect(entryObjectKey(PREFIX, "blog/post")).toBe(
      "prod/proj/web/BUILD1/cache/blog/post.cache.json",
    );
  });

  it("refuses a key that could climb out of the prefix", () => {
    expect(entryObjectKey(PREFIX, "../other/entry")).toBeNull();
    expect(entryObjectKey(PREFIX, "blog/../../escape")).toBeNull();
    expect(entryObjectKey(PREFIX, "/absolute")).toBeNull();
    expect(entryObjectKey(PREFIX, "blog//post")).toBeNull();
    expect(entryObjectKey(PREFIX, "blog/./post")).toBeNull();
    expect(entryObjectKey(PREFIX, "blog\\post")).toBeNull();
    expect(entryObjectKey(PREFIX, "")).toBeNull();
  });
});

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
