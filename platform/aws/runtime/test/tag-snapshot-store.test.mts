import { afterEach, beforeEach, expect, test, vi } from "vitest";
import {
  newTagSnapshotBucket,
  serveS3From,
  type TagSnapshotBucket,
} from "./tag-snapshot-bucket.mjs";

const prefix = "prod/proj/app/r3f8a1c9d/isr";
const snapshotKey = `${prefix}/tag-clock.json`;
const genesis = { version: 1, deployedAt: 100, generatedAt: 100, records: {} };

let bucket: TagSnapshotBucket;

beforeEach(() => {
  process.env.OCEL_ISR_BUCKET = "assets";
  process.env.OCEL_ISR_PREFIX = prefix;
  serveS3From(() => bucket);
});

afterEach(() => {
  vi.resetModules();
  vi.restoreAllMocks();
});

async function newPublisher() {
  const { newAwsTagPublisher } = await import("../src/next/tag-snapshot-store.mjs");
  return newAwsTagPublisher();
}

test("a published tag reaches the snapshot every other instance syncs from", async () => {
  bucket = newTagSnapshotBucket(snapshotKey, genesis);
  const publish = await newPublisher();

  await publish("products", { expired: 5_000 });

  expect(bucket.readRecords()).toEqual({ products: { expired: 5_000 } });
});

test("tags published together land in one snapshot write", async () => {
  bucket = newTagSnapshotBucket(snapshotKey, genesis);
  const publish = await newPublisher();

  await Promise.all([publish("cart", { expired: 4_000 }), publish("products", { expired: 4_000 })]);

  expect(bucket.objects.get(snapshotKey)!.etag).toBe('"v2"');
  expect(bucket.readRecords()).toEqual({
    cart: { expired: 4_000 },
    products: { expired: 4_000 },
  });
});

test("a publish that loses the snapshot's version to other writers tries again", async () => {
  bucket = newTagSnapshotBucket(snapshotKey, genesis);
  bucket.loseNextWrites(4);
  const publish = await newPublisher();

  await publish("products", { expired: 5_000 });

  expect(bucket.readRecords()).toEqual({ products: { expired: 5_000 } });
});

test("a publish whose conditional write conflicts with a concurrent one tries again", async () => {
  bucket = newTagSnapshotBucket(snapshotKey, genesis);
  bucket.conflictNextWrites(2);
  const publish = await newPublisher();

  await publish("products", { expired: 5_000 });

  expect(bucket.readRecords()).toEqual({ products: { expired: 5_000 } });
});

test("a build seeded with no snapshot publishes none", async () => {
  bucket = newTagSnapshotBucket(snapshotKey, null);
  const publish = await newPublisher();

  await publish("products", { expired: 5_000 });

  expect(bucket.objects.has(snapshotKey)).toBe(false);
});
