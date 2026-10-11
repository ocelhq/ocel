import type { PublishTag } from "@framework/next-cache";
import type { CacheStore } from "@framework/next-runtime/cache-store";
import { afterEach, beforeEach, expect, test, vi } from "vitest";

beforeEach(() => {
  process.env.OCEL_ISR_BUCKET = "assets";
  process.env.OCEL_ISR_PREFIX = "prod/proj/app/BID";
  process.env.OCEL_STATE_TABLE = "state";
  process.env.OCEL_ISR_TAG_NAMESPACE = "PROJECT#proj#STACK#prod--app--r3f8a1c9d#TAG#";
  delete process.env.OCEL_ISR_EDGE_READABLE;
});

afterEach(async () => {
  (await import("@framework/next-runtime/tag-clock")).setTagClockStore(null);
  vi.useRealTimers();
  vi.resetModules();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

const TABLE = "state";

const publishNothing: PublishTag = async () => {};

async function storeWithResponses(responses: any[], publish: PublishTag = publishNothing) {
  const sends: any[] = [];
  vi.doMock("@aws-sdk/client-dynamodb", async (orig) => {
    const actual = await orig<any>();
    return {
      ...actual,
      DynamoDBClient: class {
        async send(cmd: any) {
          sends.push(cmd.input);
          const next = responses.shift();
          if (!next) throw new Error("unexpected extra send()");
          if (next instanceof Error) throw next;
          return next;
        }
      },
    };
  });
  vi.doMock("@aws-sdk/client-s3", async (orig) => {
    const actual = await orig<any>();
    return {
      ...actual,
      S3Client: class {
        async send() {
          return {};
        }
      },
    };
  });
  const { newAwsCacheStore } = await import("../src/next/cache-store.mjs");
  return { store: newAwsCacheStore(publish), sends };
}

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
  return { store: newAwsCacheStore(publishNothing), built, sent, ddbSent };
}

test("refuses a key that would address something outside the deploy's prefix", async () => {
  const { store } = await entryStore();

  await expect(store.readEntry("../other/index")).rejects.toThrow("not addressable");
});

test("keeps fetch entries on the provider's bucket", async () => {
  const { store, built, sent } = await entryStore([
    { Body: { transformToString: async () => `{"lastModified":9,"value":{}}` } },
  ]);

  expect(await store.readFetch("deadbeef")).toEqual({
    lastModified: 9,
    value: {},
  });
  expect(sent[0]).toMatchObject({
    Bucket: "assets",
    Key: "prod/proj/app/BID/fetch-cache/deadbeef.cache.json",
  });
  expect(built).toHaveLength(1);
  expect(built[0].endpoint).toBeUndefined();
  expect(built[0].credentials).toBeUndefined();
});

test("writes fetch entries to the provider's bucket under the fetch prefix", async () => {
  const { store, sent } = await entryStore();

  await store.writeFetch("deadbeef", { lastModified: 1, value: {} });

  expect(sent[0]).toMatchObject({
    Bucket: "assets",
    Key: "prod/proj/app/BID/fetch-cache/deadbeef.cache.json",
  });
});

test("keeps page entries on the provider's own bucket", async () => {
  const { store, built, sent } = await entryStore();

  await store.writeEntry("index", { lastModified: 1, value: {} });

  expect(built[0].endpoint).toBeUndefined();
  expect(built[0].credentials).toBeUndefined();
  expect(sent[0]).toMatchObject({
    Bucket: "assets",
    Key: "prod/proj/app/BID/cache/index.cache.json",
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
    "prod/proj/app/BID/cache/index.cache.json",
    "prod/proj/app/BID/tag-clock.json",
  ]);
  expect(ddbSent).toEqual([]);
});

test("indexes the tag record a singular revalidateTag writes", async () => {
  const { store, sends } = await storeWithResponses([{}]);
  const handler = await handlerOver(store);
  vi.useFakeTimers();
  vi.setSystemTime(1700);

  await handler.revalidateTag("products");

  expect(sends[0]).toMatchObject({
    TableName: TABLE,
    Key: { pk: { S: "PROJECT#proj#STACK#prod--app--r3f8a1c9d#TAG#products" }, sk: { S: "#META" } },
    ConditionExpression: "attribute_not_exists(expired) OR expired < :expired",
    UpdateExpression: "SET tag = :tag, gsi1pk = :ns, gsi1sk = :writtenAt, expired = :expired",
    ExpressionAttributeValues: {
      ":tag": { S: "products" },
      ":ns": { S: "PROJECT#proj#STACK#prod--app--r3f8a1c9d#TAG#" },
      ":writtenAt": { S: "000000000001700" },
      ":expired": { N: "1700" },
    },
  });
});

test("publishes each revalidated tag's record to the instance's tag snapshot", async () => {
  const published: Array<[string, unknown]> = [];
  const { store } = await storeWithResponses([{}, {}], async (tag, record) => {
    published.push([tag, record]);
  });

  await store.writeTags(["products", "cart"], { expired: 5_000 });

  expect(published).toEqual([
    ["products", { expired: 5_000 }],
    ["cart", { expired: 5_000 }],
  ]);
});

test("does not surface a rejected guard as a failure", async () => {
  const rejected = Object.assign(new Error("guard"), {
    name: "ConditionalCheckFailedException",
  });
  const { store } = await storeWithResponses([rejected]);

  await expect(store.writeTags(["products"], { expired: 5 })).resolves.toBeUndefined();
});

test("surfaces a write failure that is not the guard", async () => {
  const { store } = await storeWithResponses([new Error("dynamo is down")]);

  await expect(store.writeTags(["products"], { expired: 5 })).rejects.toThrow(/down/);
});

function fakeTable() {
  const items = new Map<string, Record<string, any>>();

  const send = (input: any) => {
    const item = items.get(input.Key.pk.S) ?? { ...input.Key };
    const guard = /attribute_not_exists\((\w+)\) OR \w+ < (:\w+)/.exec(input.ConditionExpression);
    if (guard && item[guard[1]]) {
      const incoming = Number(input.ExpressionAttributeValues[guard[2]].N);
      if (Number(item[guard[1]].N) >= incoming) {
        throw Object.assign(new Error("guard"), {
          name: "ConditionalCheckFailedException",
        });
      }
    }
    for (const [, attr, value] of input.UpdateExpression.matchAll(/(\w+) = (:\w+)/g)) {
      item[attr] = input.ExpressionAttributeValues[value];
    }
    items.set(input.Key.pk.S, item);
    return {};
  };
  return { send, items };
}

async function storesOverTable() {
  const table = fakeTable();
  vi.doMock("@aws-sdk/client-dynamodb", async (orig) => {
    const actual = await orig<any>();
    return {
      ...actual,
      DynamoDBClient: class {
        async send(cmd: any) {
          return table.send(cmd.input);
        }
      },
    };
  });
  vi.doMock("@aws-sdk/client-s3", async (orig) => {
    const actual = await orig<any>();
    return {
      ...actual,
      S3Client: class {
        async send() {
          return {};
        }
      },
    };
  });
  const { newAwsCacheStore } = await import("../src/next/cache-store.mjs");
  const { newAwsUseCacheStore } = await import("../src/next/use-cache-store.mjs");
  return {
    store: newAwsCacheStore(publishNothing),
    useStore: newAwsUseCacheStore(publishNothing),
    items: table.items,
  };
}

test("makes a classic-model invalidation visible to the stream publisher", async () => {
  const { store, items } = await storesOverTable();

  await (await handlerOver(store)).revalidateTag("products");

  const item = items.get("PROJECT#proj#STACK#prod--app--r3f8a1c9d#TAG#products")!;
  expect(item.tag.S).toBe("products");
  expect(item.gsi1pk.S).toBe("PROJECT#proj#STACK#prod--app--r3f8a1c9d#TAG#");
  expect(Number(item.expired.N)).toBeGreaterThan(0);
  expect(Number(item.gsi1sk.S)).toBeGreaterThan(0);
});

test("an older singular write cannot walk back a newer plural one", async () => {
  const { store, useStore, items } = await storesOverTable();

  await useStore.writeTag("products", { expired: 2_000, writtenAt: 2_000 });
  await store.writeTags(["products"], { expired: 1_000 });

  const item = items.get("PROJECT#proj#STACK#prod--app--r3f8a1c9d#TAG#products")!;
  expect(item.expired.N).toBe("2000");
  expect(Number(item.gsi1sk.S)).toBe(2_000);
});

const edgeReadableTagging = "ocel%3Aedge=readable";

const putsOf = (sent: any[]) => sent.filter((input) => input.Body !== undefined);

test("tags a page entry edge-readable when the edge reads the asset bucket with its own key", async () => {
  process.env.OCEL_ISR_EDGE_READABLE = "1";
  const { store, sent } = await entryStore();

  await store.writeEntry("blog/a", { lastModified: 1, value: { kind: "APP_PAGE" } });

  const [put] = putsOf(sent);
  expect(put.Key).toBe("prod/proj/app/BID/cache/blog/a.cache.json");
  expect(put.Tagging).toBe(edgeReadableTagging);
});

test("never tags an image entry edge-readable, since the edge serves no image from the store", async () => {
  process.env.OCEL_ISR_EDGE_READABLE = "1";
  const { store, sent } = await entryStore();

  await store.writeEntry("_next/image/abc", { lastModified: 1, value: { kind: "IMAGE" } });

  expect(putsOf(sent)[0].Tagging).toBeUndefined();
});

test("never tags a data-cache entry edge-readable, however the edge reads", async () => {
  process.env.OCEL_ISR_EDGE_READABLE = "1";
  const { store, sent } = await entryStore();

  await store.writeFetch("hash1", { lastModified: 1, value: { kind: "FETCH", data: {} } });

  const [put] = putsOf(sent);
  expect(put.Key).toBe("prod/proj/app/BID/fetch-cache/hash1.cache.json");
  expect(put.Tagging).toBeUndefined();
});

test("tags nothing when the edge holds no key to the asset bucket", async () => {
  delete process.env.OCEL_ISR_EDGE_READABLE;
  const { store, sent } = await entryStore();

  await store.writeEntry("blog/a", { lastModified: 1, value: { kind: "APP_PAGE" } });

  expect(putsOf(sent)[0].Tagging).toBeUndefined();
});
