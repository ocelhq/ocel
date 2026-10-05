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

test("the ISR and use-cache stores of one host publish tags revalidated together in one snapshot write", async () => {
  const prefix = "prod/proj/app/r3f8a1c9d/isr";
  const snapshotKey = `${prefix}/tag-clock.json`;
  const bucket = newTagSnapshotBucket(snapshotKey, {
    version: 1,
    deployedAt: 100,
    generatedAt: 100,
    records: {},
  });
  vi.stubEnv("OCEL_ISR_BUCKET", "assets");
  vi.stubEnv("OCEL_ISR_PREFIX", prefix);
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
  const [cacheStore, useCacheStore] = await Promise.all([
    host.newCacheStore!(),
    host.newUseCacheStore!(),
  ]);

  await Promise.all([
    cacheStore.writeTags(["products"], { expired: 4_000 }),
    useCacheStore.writeTag("cart", { expired: 4_000, writtenAt: 4_000 }),
  ]);

  expect(bucket.objects.get(snapshotKey)!.etag).toBe('"v2"');
  expect(bucket.readRecords()).toEqual({
    cart: { expired: 4_000 },
    products: { expired: 4_000 },
  });
});

test("a failed publisher setup is tried again on the next store open", async () => {
  const prefix = "prod/proj/app/r3f8a1c9d/isr";
  vi.stubEnv("OCEL_ISR_PREFIX", prefix);
  vi.stubEnv("OCEL_STATE_TABLE", "state");
  vi.stubEnv("OCEL_ISR_TAG_NAMESPACE", "PROJECT#proj#STACK#prod--app--r3f8a1c9d#TAG#");
  serveS3From(() =>
    newTagSnapshotBucket(`${prefix}/tag-clock.json`, {
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

  await expect(host.newCacheStore!()).rejects.toThrow("OCEL_ISR_BUCKET");

  vi.stubEnv("OCEL_ISR_BUCKET", "assets");
  await expect(host.newUseCacheStore!()).resolves.toBeDefined();
});
