import type { PublishTag } from "@framework/next-cache";
import type { CacheStore } from "@framework/next-runtime/cache-store";
import { afterEach, beforeEach, expect, test, vi } from "vitest";

beforeEach(() => {
  process.env.OCEL_EDGE_BUCKET = "edge";
  process.env.OCEL_CACHE_BUCKET = "cache";
  process.env.OCEL_STORAGE_COORDINATE = "proj/prod/app/r3f8a1c9d";
  process.env.OCEL_STATE_TABLE = "state";
  process.env.OCEL_ISR_TAG_NAMESPACE = "PROJECT#proj#STACK#prod--app--r3f8a1c9d#TAG#";
});

afterEach(async () => {
  (await import("@framework/next-runtime/tag-clock")).setTagClockStore(null);
  vi.useRealTimers();
  vi.resetModules();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

const publishNothing: PublishTag = async () => {};

async function handlerOver(store: CacheStore) {
  const { default: OcelCacheHandler } = await import("@framework/next-runtime/cache-handler");
  OcelCacheHandler.store = Promise.resolve(store);
  return new OcelCacheHandler();
}

async function entryStore(responses: any[] = []) {
  const built: any[] = [];
  const sent: any[] = [];
  vi.doMock("@aws-sdk/client-s3", async (orig) => {
    const actual = await orig<any>();
    return {
      ...actual,
      S3Client: class {
        constructor(cfg: any) {
          built.push(cfg);
        }
        async send(cmd: any) {
          sent.push(cmd.input);
          return responses.shift() ?? {};
        }
      },
    };
  });
  const ddbSent: any[] = [];
  vi.doMock("@aws-sdk/client-dynamodb", async (orig) => {
    const actual = await orig<any>();
    return {
      ...actual,
      DynamoDBClient: class {
        async send(cmd: any) {
          ddbSent.push(cmd.input);
          return {};
        }
      },
    };
  });
  const { newAwsCacheStore } = await import("../src/next/cache-store.mjs");
  return { store: newAwsCacheStore(), built, sent, ddbSent };
}

test("refuses a key that would address something outside the deploy's prefix", async () => {
  const { store } = await entryStore();

  await expect(store.readEntry("../other/index")).rejects.toThrow("not addressable");
});

test("keeps fetch entries on the provider's cache bucket", async () => {
  const { store, built, sent } = await entryStore([
    { Body: { transformToString: async () => `{"lastModified":9,"value":{}}` } },
  ]);

  expect(await store.readFetch("deadbeef")).toEqual({
    lastModified: 9,
    value: {},
  });
  expect(sent[0]).toMatchObject({
    Bucket: "cache",
    Key: "fetch-cache/proj/prod/app/r3f8a1c9d/deadbeef.cache.json",
  });
  expect(built).toHaveLength(1);
  expect(built[0].endpoint).toBeUndefined();
  expect(built[0].credentials).toBeUndefined();
});

test("writes fetch entries to the provider's cache bucket under the fetch prefix", async () => {
  const { store, sent } = await entryStore();

  await store.writeFetch("deadbeef", { lastModified: 1, value: {} });

  expect(sent[0]).toMatchObject({
    Bucket: "cache",
    Key: "fetch-cache/proj/prod/app/r3f8a1c9d/deadbeef.cache.json",
  });
});

test("keeps page entries on the provider's edge bucket", async () => {
  const { store, built, sent } = await entryStore();

  await store.writeEntry("index", { lastModified: 1, value: {} });

  expect(built[0].endpoint).toBeUndefined();
  expect(built[0].credentials).toBeUndefined();
  expect(sent[0]).toMatchObject({
    Bucket: "edge",
    Key: "pages/proj/prod/app/r3f8a1c9d/index.cache.json",
  });
});

test("serves a tagged entry off the snapshot, sending nothing to the table", async () => {
  const entryBody = JSON.stringify({
    lastModified: 5_000,
    value: {
      kind: "APP_PAGE",
      html: "<html>hi</html>",
      status: 200,
      headers: { "x-next-cache-tags": "products" },
    },
  });
  const snapshotBody = JSON.stringify({
    version: 1,
    deployedAt: 1,
    generatedAt: 2,
    records: { products: { expired: 4_000 } },
  });
  const { store, sent, ddbSent } = await entryStore([
    { Body: { transformToString: async () => entryBody } },
    { Body: { transformToString: async () => snapshotBody }, ETag: '"v1"' },
  ]);
  const clock = await import("@framework/next-runtime/tag-clock");
  const { newAwsUseCacheStore } = await import("../src/next/use-cache-store.mjs");
  clock.setTagClockStore(newAwsUseCacheStore(publishNothing));

  const entry = await (await handlerOver(store)).get("/", { kind: "APP_PAGE" });

  expect(entry).not.toBeNull();
  expect(sent.map((s: any) => s.Key)).toEqual([
    "pages/proj/prod/app/r3f8a1c9d/index.cache.json",
    "tag-clock/proj/prod/app/r3f8a1c9d/tag-clock.json",
  ]);
  expect(ddbSent).toEqual([]);
});

const putsOf = (sent: any[]) => sent.filter((input) => input.Body !== undefined);

test("writes a page entry untagged into the edge bucket, which the edge reads whole", async () => {
  const { store, sent } = await entryStore();

  await store.writeEntry("blog/a", { lastModified: 1, value: { kind: "APP_PAGE" } });

  const [put] = putsOf(sent);
  expect(put.Bucket).toBe("edge");
  expect(put.Key).toBe("pages/proj/prod/app/r3f8a1c9d/blog/a.cache.json");
  expect(put.Tagging).toBeUndefined();
});

test("writes a data-cache entry untagged into the cache bucket, which no edge reads", async () => {
  const { store, sent } = await entryStore();

  await store.writeFetch("hash1", { lastModified: 1, value: { kind: "FETCH", data: {} } });

  const [put] = putsOf(sent);
  expect(put.Bucket).toBe("cache");
  expect(put.Key).toBe("fetch-cache/proj/prod/app/r3f8a1c9d/hash1.cache.json");
  expect(put.Tagging).toBeUndefined();
});
