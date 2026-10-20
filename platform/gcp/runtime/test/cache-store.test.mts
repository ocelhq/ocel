import { expect, test, vi } from "vitest";
import { newGcpCacheStore } from "../src/next/cache-store.mjs";
import { newCloudStorage } from "../src/next/cloud-storage.mjs";
import { newCloudStorageBucket } from "./cloud-storage-bucket.mjs";

const coordinate = { project: "shop", env: "prod", app: "web", release: "r1a2b3c4d" };

const entry = { lastModified: 1, value: { kind: "FETCH", data: {} } };

function openBucket(name: string) {
  const bucket = newCloudStorageBucket();
  const storage = newCloudStorage({
    bucket: name,
    endpoint: "http://storage.test",
    fetch: bucket.fetch,
    sleep: async () => {},
  });
  return { bucket, storage };
}

function open() {
  const edge = openBucket("e");
  const cache = openBucket("c");
  return {
    edge: edge.bucket,
    cache: cache.bucket,
    storage: { edge: edge.storage, cache: cache.storage },
  };
}

test("a page entry is read from the edge bucket's object its key names under the release's pages prefix", async () => {
  const { edge, storage } = open();
  edge.objects.set("pages/shop/prod/web/r1a2b3c4d/blog.cache.json", {
    body: JSON.stringify(entry),
    generation: "1",
  });
  const store = newGcpCacheStore(storage, coordinate);

  expect(await store.readEntry("blog")).toEqual(entry);
});

test("a page entry that was never written reads as null", async () => {
  const { storage } = open();
  const store = newGcpCacheStore(storage, coordinate);

  expect(await store.readEntry("blog")).toBeNull();
});

test("a page entry written is read back by the same key", async () => {
  const { storage } = open();
  const store = newGcpCacheStore(storage, coordinate);

  await store.writeEntry("blog", entry);

  expect(await store.readEntry("blog")).toEqual(entry);
});

test("a key that climbs out of the pages prefix is refused", async () => {
  const { storage } = open();
  const store = newGcpCacheStore(storage, coordinate);

  await expect(store.readEntry("../x")).rejects.toThrow(/not addressable/);
});

test("a page entry written lands in the edge bucket and nowhere else", async () => {
  const { edge, cache, storage } = open();
  const store = newGcpCacheStore(storage, coordinate);

  await store.writeEntry("blog", entry);

  expect([...edge.objects.keys()]).toEqual(["pages/shop/prod/web/r1a2b3c4d/blog.cache.json"]);
  expect(cache.objects.size).toBe(0);
});

test("a fetch entry lives in the cache bucket under fetch-cache by its hash", async () => {
  const { edge, cache, storage } = open();
  const store = newGcpCacheStore(storage, coordinate);

  await store.writeFetch("abc", entry);

  expect([...cache.objects.keys()]).toEqual(["fetch-cache/shop/prod/web/r1a2b3c4d/abc.cache.json"]);
  expect(edge.objects.size).toBe(0);
  expect(await store.readFetch("abc")).toEqual(entry);
});
