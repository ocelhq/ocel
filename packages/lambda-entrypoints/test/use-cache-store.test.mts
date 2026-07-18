import { afterEach, beforeEach, expect, test, vi } from "vitest";

// The store binds its clients from env at construction, so every test needs the
// table, namespace and index it keys into.
beforeEach(() => {
  process.env.OCEL_STATE_TABLE = "state";
  process.env.OCEL_ISR_TAG_NAMESPACE = "TAG#prod#proj#app#BID#";
  process.env.OCEL_STATE_TABLE_INDEX = "gsi1";
  process.env.OCEL_ISR_BUCKET = "assets";
  process.env.OCEL_ISR_PREFIX = "prod/proj/app/BID";
});

afterEach(() => {
  vi.resetModules();
  vi.restoreAllMocks();
});

// Drives the store against a scripted DynamoDB: each entry is one send()
// response, so a test can hand back a LastEvaluatedKey the way a truncated
// Query does. A response that is an Error is thrown instead.
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
  const { awsUseCacheStore } = await import("../src/next/use-cache-store.mjs");
  return { store: awsUseCacheStore(), sends };
}

test("writes a tag record under the monotonic guard", async () => {
  const { store, sends } = await storeWithResponses([{}]);

  const applied = await store.writeTag("products", {
    stale: 1700,
    expired: 1800,
    writtenAt: 1700,
  });

  expect(applied).toBe(true);
  expect(sends).toHaveLength(1);
  expect(sends[0]).toMatchObject({
    TableName: "state",
    Key: { pk: { S: "TAG#prod#proj#app#BID#products" }, sk: { S: "#META" } },
    ConditionExpression: "attribute_not_exists(expired) OR expired < :expired",
    UpdateExpression:
      "SET expired = :expired, stale = :stale, tag = :tag, gsi1pk = :ns, gsi1sk = :writtenAt",
    ExpressionAttributeValues: {
      ":expired": { N: "1800" },
      ":stale": { N: "1700" },
      ":tag": { S: "products" },
      ":ns": { S: "TAG#prod#proj#app#BID#" },
      ":writtenAt": { S: "000000000001700" },
    },
  });
});

// Next fans updateTags out to every registered handler, so the second write for
// one event always loses the guard. That is the common path, not an error.
test("reports a rejected conditional write rather than throwing", async () => {
  const rejected = Object.assign(new Error("guard"), {
    name: "ConditionalCheckFailedException",
  });
  const { store } = await storeWithResponses([rejected]);

  await expect(
    store.writeTag("products", { expired: 5, writtenAt: 5 }),
  ).resolves.toBe(false);
});

test("surfaces failures that are not the guard", async () => {
  const { store } = await storeWithResponses([new Error("dynamo is down")]);

  await expect(
    store.writeTag("products", { expired: 5, writtenAt: 5 }),
  ).rejects.toThrow(/down/);
});

test("queries the index for records written since the cursor", async () => {
  const { store, sends } = await storeWithResponses([{ Items: [] }]);

  await store.queryTagRecords(1700);

  expect(sends[0]).toMatchObject({
    TableName: "state",
    IndexName: "gsi1",
    KeyConditionExpression: "gsi1pk = :ns AND gsi1sk > :since",
    ExpressionAttributeValues: {
      ":ns": { S: "TAG#prod#proj#app#BID#" },
      ":since": { S: "000000000001700" },
    },
  });
  expect(sends[0].Limit).toBeGreaterThan(0);
  expect(sends[0].ExclusiveStartKey).toBeUndefined();
});

test("reads tag records back off the index projection", async () => {
  const { store } = await storeWithResponses([
    {
      Items: [
        {
          tag: { S: "products" },
          stale: { N: "1700" },
          expired: { N: "1800" },
          gsi1sk: { S: "000000000001700" },
        },
        { tag: { S: "reviews" }, gsi1sk: { S: "000000000001900" } },
      ],
    },
  ]);

  const page = await store.queryTagRecords(0);

  expect(page.records).toEqual([
    { tag: "products", stale: 1700, expired: 1800, writtenAt: 1700 },
    { tag: "reviews", stale: undefined, expired: undefined, writtenAt: 1900 },
  ]);
  expect(page.cursor).toBeUndefined();
});

test("hands back the response cursor and feeds it into the next page", async () => {
  const last = { gsi1pk: { S: "TAG#prod#proj#app#BID#" }, gsi1sk: { S: "000000000001700" } };
  const { store, sends } = await storeWithResponses([
    { Items: [{ tag: { S: "a" }, gsi1sk: { S: "000000000001700" } }], LastEvaluatedKey: last },
    { Items: [{ tag: { S: "b" }, gsi1sk: { S: "000000000001800" } }] },
  ]);

  const first = await store.queryTagRecords(0);
  expect(first.cursor).toEqual(last);

  const second = await store.queryTagRecords(0, first.cursor);
  expect(second.cursor).toBeUndefined();
  expect(sends[1].ExclusiveStartKey).toEqual(last);
});

// Drives the store against a scripted S3 the same way, so the object key layout
// and the envelope round-trip are asserted on the command actually emitted.
async function storeWithObjects(responses: any[]) {
  const sends: any[] = [];
  vi.doMock("@aws-sdk/client-s3", async (orig) => {
    const actual = await orig<any>();
    return {
      ...actual,
      S3Client: class {
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
  const { awsUseCacheStore } = await import("../src/next/use-cache-store.mjs");
  return { store: awsUseCacheStore(), sends };
}

const envelope = {
  tags: ["products"],
  stale: 30,
  timestamp: 1700,
  expire: 3600,
  revalidate: 60,
  body: Buffer.from("payload").toString("base64"),
};

const objectBody = (value: unknown) => ({
  Body: { transformToString: async () => JSON.stringify(value) },
});

// The cache key Next hands a handler is an encodeReply blob of arbitrary bytes
// and arbitrary length, which is not a legal object key.
test("hashes the cache key into a legal object name under the build prefix", async () => {
  const { store, sends } = await storeWithObjects([{}]);
  const cacheKey = "\u0000binary\uffff" + "x".repeat(4096);

  await store.writeEntry(cacheKey, envelope);

  expect(sends[0].Bucket).toBe("assets");
  expect(sends[0].Key).toMatch(/^prod\/proj\/app\/BID\/use-cache\/[0-9a-f]{64}\.json$/);
  expect(sends[0].Key).not.toContain("binary");
});

test("keys the same entry identically on write and read", async () => {
  const { store, sends } = await storeWithObjects([{}, objectBody(envelope)]);

  await store.writeEntry("k", envelope);
  await store.readEntry("k");

  expect(sends[1].Key).toBe(sends[0].Key);
});

test("gives distinct keys distinct object names", async () => {
  const { store, sends } = await storeWithObjects([{}, {}]);

  await store.writeEntry("a", envelope);
  await store.writeEntry("b", envelope);

  expect(sends[1].Key).not.toBe(sends[0].Key);
});

// One JSON document per entry: one round-trip to read, and an atomic write with
// no torn entry to serve.
test("round-trips the entry as a single JSON envelope", async () => {
  const { store, sends } = await storeWithObjects([{}]);

  await store.writeEntry("k", envelope);

  expect(sends[0].ContentType).toBe("application/json");
  expect(JSON.parse(sends[0].Body)).toEqual(envelope);
});

test("reads an entry back off the stored envelope", async () => {
  const { store } = await storeWithObjects([objectBody(envelope)]);

  expect(await store.readEntry("k")).toEqual(envelope);
});

test("reports an absent object as a miss rather than a failure", async () => {
  const missing = Object.assign(new Error("nope"), { name: "NoSuchKey" });
  const { store } = await storeWithObjects([missing]);

  await expect(store.readEntry("k")).resolves.toBeNull();
});

// Anything that is not a 404 is a real outage, and the handler is what turns it
// into a miss — the store must not disguise it as an absent entry.
test("surfaces a read failure that is not an absent object", async () => {
  const { store } = await storeWithObjects([new Error("s3 is down")]);

  await expect(store.readEntry("k")).rejects.toThrow(/down/);
});
