import { afterEach, beforeEach, expect, test, vi } from "vitest";
import type { CacheStore } from "../src/next/cache-store.mjs";

// What the membrane injects when this substrate's edge offered a cache store.
// All five are set together or none is.
const storeEnv = {
  OCEL_ISR_STORE_BUCKET: "isr",
  OCEL_ISR_STORE_ENDPOINT: "https://acct.r2.cloudflarestorage.com",
  OCEL_ISR_STORE_REGION: "auto",
  OCEL_ISR_STORE_ACCESS_KEY_ID: "AK",
  OCEL_ISR_STORE_SECRET_ACCESS_KEY: "s3cret",
};

const adoptStore = () => Object.assign(process.env, storeEnv);

// The store binds its clients from env at construction, so every test needs the
// namespace it keys into — and the index the plural store reads back out of.
// The adopted-store vars start absent: a test that wants one opts in.
beforeEach(() => {
  process.env.OCEL_ISR_BUCKET = "assets";
  process.env.OCEL_ISR_PREFIX = "prod/proj/app/BID";
  process.env.OCEL_STATE_TABLE = "state";
  process.env.OCEL_ISR_TAG_NAMESPACE = "TAG#prod#proj#app#BID#";
  process.env.OCEL_STATE_TABLE_INDEX = "gsi1";
  for (const name of Object.keys(storeEnv)) delete process.env[name];
});

afterEach(() => {
  vi.useRealTimers();
  vi.resetModules();
  vi.restoreAllMocks();
});

const TABLE = "state";

function tagItem(tag: string, expired: number) {
  return {
    pk: { S: `TAG#prod#proj#app#BID#${tag}` },
    sk: { S: "#META" },
    expired: { N: String(expired) },
  };
}

// Drives the store against a scripted DynamoDB: each entry is one send()
// response, so a test can hand back UnprocessedKeys the way a throttled
// BatchGetItem does. A response that is an Error is thrown instead.
async function storeWithResponses(responses: any[]) {
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
    return { ...actual, S3Client: class { async send() { return {}; } } };
  });
  const { awsCacheStore } = await import("../src/next/cache-store.mjs");
  return { store: awsCacheStore(), sends };
}

// The handler comes out of the same mocked graph as the store it is bound to, so
// its lazy binding never reaches for the real clients.
async function handlerOver(store: CacheStore) {
  const { default: OcelCacheHandler } = await import("../src/next/cache-handler.mjs");
  OcelCacheHandler.store = store;
  return new OcelCacheHandler();
}

// Drives the entry path against a scripted S3-compatible client, capturing both
// how the client was constructed and what it was sent — which bucket an entry
// lands in, and at which key, is the whole question here.
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
  vi.doMock("@aws-sdk/client-dynamodb", async (orig) => {
    const actual = await orig<any>();
    return { ...actual, DynamoDBClient: class { async send() { return {}; } } };
  });
  const { awsCacheStore } = await import("../src/next/cache-store.mjs");
  return { store: awsCacheStore(), built, sent };
}

test("reads entries out of the adopted cache store when one is injected", async () => {
  adoptStore();
  const { store, built, sent } = await entryStore([
    { Body: { transformToString: async () => `{"lastModified":1,"value":{}}` } },
  ]);

  expect(await store.readEntry("index")).toEqual({ lastModified: 1, value: {} });
  expect(built[0]).toMatchObject({
    region: "auto",
    endpoint: "https://acct.r2.cloudflarestorage.com",
    credentials: { accessKeyId: "AK", secretAccessKey: "s3cret" },
  });
  expect(sent[0]).toMatchObject({
    Bucket: "isr",
    Key: "prod/proj/app/BID/cache/index.cache.json",
  });
});

// The edge reads exactly this key out of exactly this bucket, so origin and edge
// meet on one object or they disagree about what is cached.
test("writes entries to the adopted cache store at the key the edge reads", async () => {
  adoptStore();
  const { store, sent } = await entryStore();

  await store.writeEntry("blog/post", { lastModified: 1, value: {} });

  expect(sent[0]).toMatchObject({
    Bucket: "isr",
    Key: "prod/proj/app/BID/cache/blog/post.cache.json",
  });
});

// The rollback for the whole colocation: a substrate whose edge offered no store
// injects nothing, and ISR stays exactly where it was, on the provider's own
// bucket under the provider's own credential chain.
test("stays on the provider's own bucket when no store is adopted", async () => {
  const { store, built, sent } = await entryStore();

  await store.writeEntry("index", { lastModified: 1, value: {} });

  expect(built[0].endpoint).toBeUndefined();
  expect(built[0].credentials).toBeUndefined();
  expect(sent[0]).toMatchObject({
    Bucket: "assets",
    Key: "prod/proj/app/BID/cache/index.cache.json",
  });
});

// DynamoDB stays the one true clock. Adopting a store moves entries and nothing
// else: tag reads run in Lambda, where the table is in-region, and taxing every
// origin render to subsidize the edge is the trade this epic refuses.
test("leaves the tag path on DynamoDB when a store is adopted", async () => {
  adoptStore();
  const { store, sends } = await storeWithResponses([
    { Responses: { [TABLE]: [tagItem("products", 111)] } },
  ]);

  expect((await store.readTags(["products"])).get("products")?.expired).toBe(111);
  expect(sends).toHaveLength(1);
});

test("reads tag records back out of a batch response", async () => {
  const { store } = await storeWithResponses([
    { Responses: { [TABLE]: [tagItem("products", 111)] } },
  ]);

  const found = await store.readTags(["products"]);

  expect(found.get("products")).toEqual({ stale: undefined, expired: 111 });
});

// BatchGetItem returns throttled keys in UnprocessedKeys on an otherwise
// successful response, so the SDK's own retries never see them.
test("retries keys DynamoDB left unprocessed", async () => {
  const unprocessed = { [TABLE]: { Keys: [tagItem("products", 0)] } };
  const { store, sends } = await storeWithResponses([
    { Responses: { [TABLE]: [] }, UnprocessedKeys: unprocessed },
    { Responses: { [TABLE]: [tagItem("products", 111)] } },
  ]);

  const found = await store.readTags(["products"]);

  expect(sends).toHaveLength(2);
  expect(found.get("products")?.expired).toBe(111);
});

// A tag record that never arrives is indistinguishable from a tag that was never
// revalidated, and the handler reads that as "not expired" — so giving up
// quietly would serve stale content. Throwing lets get() degrade to a miss.
test("throws rather than returning a partial tag read", async () => {
  const unprocessed = { [TABLE]: { Keys: [tagItem("products", 0)] } };
  const { store } = await storeWithResponses(
    Array.from({ length: 4 }, () => ({
      Responses: { [TABLE]: [] },
      UnprocessedKeys: unprocessed,
    })),
  );

  await expect(store.readTags(["products"])).rejects.toThrow(/unprocessed/);
});

test("splits reads over BatchGetItem's 100-key limit", async () => {
  const { store, sends } = await storeWithResponses([
    { Responses: { [TABLE]: [] } },
    { Responses: { [TABLE]: [] } },
  ]);

  await store.readTags(Array.from({ length: 150 }, (_, i) => `t${i}`));

  expect(sends).toHaveLength(2);
  expect(sends[0].RequestItems[TABLE].Keys).toHaveLength(100);
  expect(sends[1].RequestItems[TABLE].Keys).toHaveLength(50);
});

test("skips DynamoDB entirely when there are no tags", async () => {
  const { store, sends } = await storeWithResponses([]);

  expect((await store.readTags([])).size).toBe(0);
  expect(sends).toHaveLength(0);
});

// The record the singular handler writes is the same record the plural store
// writes, so it has to carry the index attributes too: a row without them is
// invisible to every delta replica reading the index.
test("indexes the tag record a singular revalidateTag writes", async () => {
  const { store, sends } = await storeWithResponses([{}]);
  const handler = await handlerOver(store);
  vi.useFakeTimers();
  vi.setSystemTime(1700);

  await handler.revalidateTag("products");

  expect(sends[0]).toMatchObject({
    TableName: TABLE,
    Key: { pk: { S: "TAG#prod#proj#app#BID#products" }, sk: { S: "#META" } },
    ConditionExpression: "attribute_not_exists(expired) OR expired < :expired",
    UpdateExpression:
      "SET tag = :tag, gsi1pk = :ns, gsi1sk = :writtenAt, expired = :expired",
    ExpressionAttributeValues: {
      ":tag": { S: "products" },
      ":ns": { S: "TAG#prod#proj#app#BID#" },
      ":writtenAt": { S: "000000000001700" },
      ":expired": { N: "1700" },
    },
  });
});

// Next hands revalidateTag straight through with no try/catch, so a rejected
// guard reaching the caller would fail the request that raised the invalidation.
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

// Enough of UpdateItem's SET and guard, and of the index Query, for a write by
// one store to be read back by the other. Nothing translates between them: the
// reader sees exactly the attributes the writer emitted.
function fakeTable() {
  const items = new Map<string, Record<string, any>>();

  return (input: any) => {
    if (input.KeyConditionExpression) {
      const ns = input.ExpressionAttributeValues[":ns"].S;
      const since = input.ExpressionAttributeValues[":since"].S;
      return {
        Items: [...items.values()]
          .filter((item) => item.gsi1pk?.S === ns && item.gsi1sk.S >= since)
          .sort((a, b) => a.gsi1sk.S.localeCompare(b.gsi1sk.S)),
      };
    }

    const item = items.get(input.Key.pk.S) ?? { ...input.Key };
    const guard = /attribute_not_exists\((\w+)\) OR \w+ < (:\w+)/.exec(
      input.ConditionExpression,
    );
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
}

// Both stores are built from the same mocked client, so they meet on one table
// exactly as the two handler tiers meet on one state table in production.
async function storesOverTable() {
  const table = fakeTable();
  vi.doMock("@aws-sdk/client-dynamodb", async (orig) => {
    const actual = await orig<any>();
    return {
      ...actual,
      DynamoDBClient: class {
        async send(cmd: any) {
          return table(cmd.input);
        }
      },
    };
  });
  vi.doMock("@aws-sdk/client-s3", async (orig) => {
    const actual = await orig<any>();
    return { ...actual, S3Client: class { async send() { return {}; } } };
  });
  const { awsCacheStore } = await import("../src/next/cache-store.mjs");
  const { awsUseCacheStore } = await import("../src/next/use-cache-store.mjs");
  return { store: awsCacheStore(), useStore: awsUseCacheStore() };
}

// The classic ISR model registers only the singular handler and has no `use
// cache` anywhere, so nothing else writes its tags. Its invalidations have to
// reach the index on their own — otherwise a Next version that stops fanning
// revalidateTag out to the plural handlers breaks replication silently.
test("makes a classic-model invalidation visible to the index reader", async () => {
  const { store, useStore } = await storesOverTable();

  await (await handlerOver(store)).revalidateTag("products");
  const page = await useStore.queryTagRecords(0);

  expect(page.records).toHaveLength(1);
  expect(page.records[0].tag).toBe("products");
  expect(page.records[0].expired).toBeGreaterThan(0);
  expect(page.records[0].writtenAt).toBeGreaterThan(0);
});

// The two tiers advance one shared watermark, which is only safe while every
// writer agrees the guard is monotonic.
test("an older singular write cannot walk back a newer plural one", async () => {
  const { store, useStore } = await storesOverTable();

  await useStore.writeTag("products", { expired: 2_000, writtenAt: 2_000 });
  await store.writeTags(["products"], { expired: 1_000 });

  const page = await useStore.queryTagRecords(0);
  expect(page.records[0]).toMatchObject({ expired: 2_000, writtenAt: 2_000 });
});
