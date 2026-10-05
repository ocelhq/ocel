import { newTagPublisher } from "@framework/next-cache";
import { expect, test, vi } from "vitest";
import { newGcpCacheStore } from "../src/next/cache-store.mjs";
import { newCloudStorage } from "../src/next/cloud-storage.mjs";
import { newCloudStorageTagSnapshotStore } from "../src/next/tag-snapshot-store.mjs";
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

test("a published tag reaches the snapshot file", async () => {
  const { bucket, storage } = open();
  const clock = `${prefix}/tag-clock.json`;
  bucket.objects.set(clock, {
    body: JSON.stringify({ version: 1, deployedAt: 1, generatedAt: 1, records: {} }),
    generation: "1",
  });
  const store = newGcpCacheStore(
    storage,
    prefix,
    newTagPublisher(newCloudStorageTagSnapshotStore(storage, prefix)),
  );

  await store.writeTags(["cart"], { expired: 5 });

  expect(JSON.parse(bucket.objects.get(clock)!.body).records.cart).toEqual({ expired: 5 });
});
