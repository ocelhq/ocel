import { expect, test } from "vitest";
import { newCloudStorage } from "../src/next/cloud-storage.mjs";
import { newCloudStorageTagSnapshotStore } from "../src/next/tag-snapshot-store.mjs";
import { newCloudStorageBucket } from "./cloud-storage-bucket.mjs";

const prefix = "cache/shop/web/prod/r1/isr";
const clockName = `${prefix}/tag-clock.json`;
const genesis = { version: 1, deployedAt: 1, generatedAt: 1, records: {} } as const;

function open() {
  const bucket = newCloudStorageBucket();
  const storage = newCloudStorage({
    bucket: "b",
    endpoint: "http://storage.test",
    fetch: bucket.fetch,
    sleep: async () => {},
  });
  return { bucket, store: newCloudStorageTagSnapshotStore(storage, prefix) };
}

test("a missing tag snapshot reads as none, so a publish writes nothing", async () => {
  const { store } = open();

  expect(await store.read()).toBeNull();
});

test("a snapshot is read with its generation as its version", async () => {
  const { bucket, store } = open();
  bucket.objects.set(clockName, { body: JSON.stringify(genesis), generation: "42" });

  expect(await store.read()).toEqual({ snapshot: genesis, etag: "42" });
});

test("a snapshot write conditioned on a replaced generation reports the version lost", async () => {
  const { bucket, store } = open();
  bucket.objects.set(clockName, { body: JSON.stringify(genesis), generation: "42" });
  const read = (await store.read())!;
  bucket.objects.set(clockName, { body: JSON.stringify(genesis), generation: "43" });

  expect(await store.write({ ...genesis, generatedAt: 2 }, read)).toBe(false);
  expect(bucket.objects.get(clockName)?.generation).toBe("43");
});

test("a snapshot write conditioned on the current generation lands", async () => {
  const { bucket, store } = open();
  bucket.objects.set(clockName, { body: JSON.stringify(genesis), generation: "42" });
  const read = (await store.read())!;

  expect(await store.write({ ...genesis, generatedAt: 2 }, read)).toBe(true);
  expect(JSON.parse(bucket.objects.get(clockName)!.body).generatedAt).toBe(2);
});

test("a snapshot at a version this publisher cannot merge into is refused", async () => {
  const { bucket, store } = open();
  bucket.objects.set(clockName, {
    body: JSON.stringify({ ...genesis, version: 2 }),
    generation: "1",
  });

  await expect(store.read()).rejects.toThrow(/not a version this publisher can merge into/);
});
