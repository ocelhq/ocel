import { afterEach, expect, test, vi } from "vitest";
import { newAwsNextHost } from "../src/next/next-host.mjs";
import { newTagSnapshotBucket, serveS3From } from "./tag-snapshot-bucket.mjs";

afterEach(() => {
  vi.unstubAllEnvs();
  vi.resetModules();
  vi.doUnmock("@aws-sdk/client-s3");
  vi.doUnmock("@aws-sdk/client-dynamodb");
});

test("the AWS host declares Lambda's task root as the function directory", () => {
  expect(newAwsNextHost({ LAMBDA_TASK_ROOT: "/var/task" }).functionDir).toBe("/var/task");
});

test("the AWS host's instance cache holds a tenth of Lambda's configured memory", () => {
  const cache = newAwsNextHost({ AWS_LAMBDA_FUNCTION_MEMORY_SIZE: "1" }).instanceCache!;

  cache.write("fits", 1, 100 * 1024);
  cache.write("over", 2, 100 * 1024);
  cache.write("huge", 3, 110 * 1024);

  expect(cache.read("fits")).toBeUndefined();
  expect(cache.read("over")).toBe(2);
  expect(cache.read("huge")).toBeUndefined();
});

test("the AWS host's instance cache holds 50 MiB when Lambda names no memory", () => {
  const cache = newAwsNextHost({}).instanceCache!;

  cache.write("fits", 1, 50 * 1024 * 1024);
  cache.write("huge", 2, 50 * 1024 * 1024 + 1);

  expect(cache.read("fits")).toBe(1);
  expect(cache.read("huge")).toBeUndefined();
});

test("the AWS host declares the 50 cache tags CloudFront stores per object", () => {
  expect(newAwsNextHost({}).cacheTagsPerObject).toBe(50);
});

test("the use-cache store of the host publishes tags revalidated together in one snapshot write", async () => {
  const snapshotKey = "tag-clock/proj/prod/app/r3f8a1c9d/tag-clock.json";
  const bucket = newTagSnapshotBucket(snapshotKey, {
    version: 1,
    deployedAt: 100,
    generatedAt: 100,
    records: {},
  });
  vi.stubEnv("OCEL_EDGE_BUCKET", "edge");
  vi.stubEnv("OCEL_CACHE_BUCKET", "cache");
  vi.stubEnv("OCEL_STORAGE_COORDINATE", "proj/prod/app/r3f8a1c9d");
  vi.stubEnv("OCEL_STATE_TABLE", "state");
  vi.stubEnv("OCEL_ISR_TAG_NAMESPACE", "PROJECT#proj#STACK#prod--app--r3f8a1c9d#TAG#");
  serveS3From(() => bucket);
  vi.doMock("@aws-sdk/client-dynamodb", async (orig) => ({
    ...(await orig<any>()),
    DynamoDBClient: class {
      async send() {
        return {};
      }
    },
  }));
  const host = newAwsNextHost({});
  const useCacheStore = await host.newUseCacheStore!();

  await Promise.all([
    useCacheStore.writeTag("products", { expired: 4_000, writtenAt: 4_000 }),
    useCacheStore.writeTag("cart", { expired: 4_000, writtenAt: 4_000 }),
  ]);

  expect(bucket.objects.get(snapshotKey)!.etag).toBe('"v2"');
  expect(bucket.readRecords()).toEqual({
    cart: { expired: 4_000 },
    products: { expired: 4_000 },
  });
});

test("a use-cache store that fails to open is opened again on the next try", async () => {
  vi.stubEnv("OCEL_STORAGE_COORDINATE", "proj/prod/app/r3f8a1c9d");
  vi.stubEnv("OCEL_STATE_TABLE", "state");
  vi.stubEnv("OCEL_ISR_TAG_NAMESPACE", "PROJECT#proj#STACK#prod--app--r3f8a1c9d#TAG#");
  serveS3From(() =>
    newTagSnapshotBucket("tag-clock/proj/prod/app/r3f8a1c9d/tag-clock.json", {
      version: 1,
      deployedAt: 100,
      generatedAt: 100,
      records: {},
    }),
  );
  vi.doMock("@aws-sdk/client-dynamodb", async (orig) => ({
    ...(await orig<any>()),
    DynamoDBClient: class {
      async send() {
        return {};
      }
    },
  }));
  const host = newAwsNextHost({});

  await expect(host.newUseCacheStore!()).rejects.toThrow("OCEL_CACHE_BUCKET");

  vi.stubEnv("OCEL_CACHE_BUCKET", "cache");
  await expect(host.newUseCacheStore!()).resolves.toBeDefined();
});
