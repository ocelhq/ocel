import { afterEach, beforeEach, expect, test, vi } from "vitest";

// The store binds its clients from env at construction, so every test needs the
// table, namespace and index it keys into.
beforeEach(() => {
  process.env.OCEL_STATE_TABLE = "state";
  process.env.OCEL_ISR_TAG_NAMESPACE = "TAG#prod#proj#app#BID#";
  process.env.OCEL_STATE_TABLE_INDEX = "gsi1";
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
