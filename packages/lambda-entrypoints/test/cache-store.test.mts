import { afterEach, beforeEach, expect, test, vi } from "vitest";

// The store binds its clients from env at construction, so every test needs the
// namespace it keys into.
beforeEach(() => {
  process.env.OCEL_ISR_BUCKET = "assets";
  process.env.OCEL_ISR_PREFIX = "prod/proj/app/BID";
  process.env.OCEL_STATE_TABLE = "state";
  process.env.OCEL_ISR_TAG_NAMESPACE = "TAG#prod#proj#app#BID#";
});

afterEach(() => {
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
// BatchGetItem does.
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
