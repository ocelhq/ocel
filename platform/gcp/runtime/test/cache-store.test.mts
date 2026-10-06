import { expect, test, vi } from "vitest";
import { newGcpCacheStore } from "../src/next/cache-store.mjs";
import { newCloudStorage } from "../src/next/cloud-storage.mjs";
import { newCloudStorageBucket } from "./cloud-storage-bucket.mjs";

const prefix = "cache/shop/web/prod/r1/isr";
const entry = { lastModified: 1, value: { kind: "FETCH", data: {} } };

function open() {
  const bucket = newCloudStorageBucket();
  const storage = newCloudStorage({
    bucket: "b",
    endpoint: "http://storage.test",
    fetch: bucket.fetch,
    sleep: async () => {},
  });
  return { bucket, storage };
}

test("a page entry is read from the object its key names under the app's cache prefix", async () => {
  const { bucket, storage } = open();
  bucket.objects.set(`${prefix}/cache/blog.cache.json`, {
    body: JSON.stringify(entry),
    generation: "1",
  });
  const store = newGcpCacheStore(storage, prefix, vi.fn());

  expect(await store.readEntry("blog")).toEqual(entry);
});

test("a page entry that was never written reads as null", async () => {
  const { storage } = open();
  const store = newGcpCacheStore(storage, prefix, vi.fn());

  expect(await store.readEntry("blog")).toBeNull();
});

test("a page entry written is read back by the same key", async () => {
  const { storage } = open();
  const store = newGcpCacheStore(storage, prefix, vi.fn());

  await store.writeEntry("blog", entry);

  expect(await store.readEntry("blog")).toEqual(entry);
});

test("a key that climbs out of the cache prefix is refused", async () => {
  const { storage } = open();
  const store = newGcpCacheStore(storage, prefix, vi.fn());

  await expect(store.readEntry("../x")).rejects.toThrow(/not addressable/);
});

test("a fetch entry lives under fetch-cache by its hash", async () => {
  const { bucket, storage } = open();
  const store = newGcpCacheStore(storage, prefix, vi.fn());

  await store.writeFetch("abc", entry);

  expect(bucket.objects.has(`${prefix}/fetch-cache/abc.cache.json`)).toBe(true);
  expect(await store.readFetch("abc")).toEqual(entry);
});

test("a tag record with neither stale nor expired publishes nothing", async () => {
  const { storage } = open();
  const publish = vi.fn();
  const store = newGcpCacheStore(storage, prefix, publish);

  await store.writeTags(["cart"], {});

  expect(publish).not.toHaveBeenCalled();
});

test("a published tag is handed to the tag records", async () => {
  const { storage } = open();
  const publish = vi.fn(async () => {});
  const store = newGcpCacheStore(storage, prefix, publish);

  await store.writeTags(["cart"], { expired: 5 });

  expect(publish).toHaveBeenCalledWith("cart", { expired: 5 });
});

test("page entries are kept in the edge's store when an isr-writer is bound", async () => {
  const { bucket, storage } = open();
  const pages = {
    readEntry: vi.fn(async () => entry),
    writeEntry: vi.fn(async () => {}),
  };
  const store = newGcpCacheStore(storage, prefix, vi.fn(), pages);

  await store.writeEntry("blog", entry);
  expect(await store.readEntry("blog")).toEqual(entry);

  expect(pages.writeEntry).toHaveBeenCalledWith("blog", entry);
  expect(pages.readEntry).toHaveBeenCalledWith("blog");
  expect([...bucket.objects.keys()]).toEqual([]);
});

test("fetch entries are kept in Cloud Storage when an isr-writer is bound", async () => {
  const { bucket, storage } = open();
  const pages = { readEntry: vi.fn(), writeEntry: vi.fn() };
  const store = newGcpCacheStore(storage, prefix, vi.fn(), pages);

  await store.writeFetch("abc", entry);

  expect(await store.readFetch("abc")).toEqual(entry);
  expect([...bucket.objects.keys()]).toEqual([`${prefix}/fetch-cache/abc.cache.json`]);
  expect(pages.writeEntry).not.toHaveBeenCalled();
});
