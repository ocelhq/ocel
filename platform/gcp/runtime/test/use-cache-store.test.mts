import { createHash } from "node:crypto";
import { expect, test } from "vitest";
import { newCloudStorage } from "../src/next/cloud-storage.mjs";
import { newFirestore } from "../src/next/firestore.mjs";
import { newFirestoreTagRecords } from "../src/next/tag-records.mjs";
import { newGcpUseCacheStore } from "../src/next/use-cache-store.mjs";
import { newCloudStorageBucket } from "./cloud-storage-bucket.mjs";
import { newFirestoreDatabase } from "./firestore-database.mjs";

const database = "projects/p/databases/d";
const prefix = "cache/shop/web/prod/r1a2b3c4d/isr";
const entry = { tags: [], stale: 1, timestamp: 2, expire: 3, revalidate: 4, body: "b" };

function open() {
  const bucket = newCloudStorageBucket();
  const fake = newFirestoreDatabase({ database });
  const instance = () =>
    newGcpUseCacheStore(
      newCloudStorage({
        bucket: "b",
        endpoint: "http://storage.test",
        fetch: bucket.fetch,
        sleep: async () => {},
      }),
      prefix,
      newFirestoreTagRecords(newFirestore({ database, fetch: fake.fetch }), "prod/shop/web/r1/isr"),
    );
  return { bucket, instance };
}

test("a tag one instance writes reaches the clock of another instance on its next read", async () => {
  const { instance } = open();
  const writer = instance();
  const reader = instance();

  expect(await writer.writeTag("posts", { stale: 1, expired: 5, writtenAt: 99 })).toBe(true);
  const read = await reader.readTagSnapshot(null);

  expect(read).toMatchObject({ status: "fresh", records: { posts: { stale: 1, expired: 5 } } });
});

test("a use-cache entry one instance writes is read by another", async () => {
  const { instance } = open();

  await instance().writeEntry("k", entry);

  expect(await instance().readEntry("k")).toEqual(entry);
});

test("a use-cache key is stored under its SHA-256, so any key is a valid object name", async () => {
  const { bucket, instance } = open();
  const key = "a/../b?#";

  await instance().writeEntry(key, entry);

  const hash = createHash("sha256").update(key).digest("hex");
  expect([...bucket.objects.keys()]).toEqual([`${prefix}/use-cache/${hash}.json`]);
});

test("a use-cache entry never written reads as null", async () => {
  const { instance } = open();

  expect(await instance().readEntry("missing")).toBeNull();
});

test("a use-cache read that keeps failing surfaces the failure", async () => {
  const { bucket, instance } = open();
  bucket.fail(5, 503);

  await expect(instance().readEntry("k")).rejects.toThrow();
});

test("a use-cache entry written twice reads back the second", async () => {
  const { instance } = open();
  const store = instance();

  await store.writeEntry("k", entry);
  await store.writeEntry("k", { ...entry, body: "second" });

  expect(await store.readEntry("k")).toMatchObject({ body: "second" });
});

test("a use-cache entry written on one instance is not served from that instance's memory", async () => {
  const { instance } = open();
  const first = instance();
  const second = instance();

  await first.writeEntry("k", entry);
  await second.writeEntry("k", { ...entry, body: "newer" });

  expect(await first.readEntry("k")).toMatchObject({ body: "newer" });
});
