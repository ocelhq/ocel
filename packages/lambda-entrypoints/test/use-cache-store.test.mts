import { afterEach, beforeEach, expect, test, vi } from "vitest";

beforeEach(() => {
  process.env.OCEL_STATE_TABLE = "state";
  process.env.OCEL_ISR_TAG_NAMESPACE = "TAG#prod#proj#app#BID#";
  process.env.OCEL_ISR_BUCKET = "assets";
  process.env.OCEL_ISR_PREFIX = "prod/proj/app/BID";
});

afterEach(() => {
  vi.resetModules();
  vi.restoreAllMocks();
  for (const v of Object.keys(process.env)) {
    if (v.startsWith("OCEL_ISR_STORE_")) delete process.env[v];
  }
});

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

test("stays on the provider's bucket when a cache store is adopted", async () => {
  Object.assign(process.env, {
    OCEL_ISR_STORE_BUCKET: "isr",
  });
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
          return {};
        }
      },
    };
  });
  const { awsUseCacheStore } = await import("../src/next/use-cache-store.mjs");

  await awsUseCacheStore().writeEntry("k", {
    tags: [],
    stale: 0,
    timestamp: 0,
    expire: 0,
    revalidate: 0,
    body: "",
  });

  expect(built[0].endpoint).toBeUndefined();
  expect(sent[0].Bucket).toBe("assets");
  expect(sent[0].Key).toMatch(/^prod\/proj\/app\/BID\/use-cache\//);
});

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
      "SET tag = :tag, gsi1pk = :ns, gsi1sk = :writtenAt, expired = :expired, stale = :stale",
    ExpressionAttributeValues: {
      ":expired": { N: "1800" },
      ":stale": { N: "1700" },
      ":tag": { S: "products" },
      ":ns": { S: "TAG#prod#proj#app#BID#" },
      ":writtenAt": { S: "000000000001700" },
    },
  });
});

test("guards a stale-only write on stale, and does not write an absent expiry", async () => {
  const { store, sends } = await storeWithResponses([{}]);

  await store.writeTag("products", { stale: 1700, writtenAt: 1700 });

  expect(sends[0]).toMatchObject({
    ConditionExpression: "attribute_not_exists(stale) OR stale < :stale",
    UpdateExpression: "SET tag = :tag, gsi1pk = :ns, gsi1sk = :writtenAt, stale = :stale",
  });
  expect(sends[0].ExpressionAttributeValues).not.toHaveProperty(":expired");
  expect(sends[0].UpdateExpression).not.toContain("expired");
});

test("rounds a fractional write time into the fixed-width sort key", async () => {
  const { store, sends } = await storeWithResponses([{}]);

  await store.writeTag("products", { expired: 9, writtenAt: 1700.6 });

  expect(sends[0].ExpressionAttributeValues[":writtenAt"]).toEqual({
    S: "000000000001701",
  });
});

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

test("surfaces a read failure that is not an absent object", async () => {
  const { store } = await storeWithObjects([new Error("s3 is down")]);

  await expect(store.readEntry("k")).rejects.toThrow(/down/);
});

const snapshot = {
  version: 1,
  deployedAt: 1_000,
  generatedAt: 1_700,
  records: { products: { expired: 1_800 }, reviews: { stale: 1_900 } },
};

const storedSnapshot = (value: unknown, etag?: string) => ({
  ...objectBody(value),
  ...(etag !== undefined ? { ETag: etag } : {}),
});

const notModified = () =>
  Object.assign(new Error("not modified"), {
    name: "NotModified",
    $metadata: { httpStatusCode: 304 },
  });

test("reads the whole tag clock from one object under the build's prefix", async () => {
  const { store, sends } = await storeWithObjects([storedSnapshot(snapshot, '"v1"')]);

  const read = await store.readTagSnapshot(null);

  expect(sends).toHaveLength(1);
  expect(sends[0].Bucket).toBe("assets");
  expect(sends[0].Key).toBe("prod/proj/app/BID/tag-clock.json");
  expect(sends[0].IfNoneMatch).toBeUndefined();
  expect(read).toEqual({
    status: "fresh",
    records: snapshot.records,
    etag: '"v1"',
  });
});

test("conditions a later read on the version it already holds", async () => {
  const { store, sends } = await storeWithObjects([notModified()]);

  const read = await store.readTagSnapshot('"v1"');

  expect(sends[0].IfNoneMatch).toBe('"v1"');
  expect(read).toEqual({ status: "unchanged" });
});

test("reads an object the store named no version for", async () => {
  const { store } = await storeWithObjects([storedSnapshot(snapshot)]);

  expect(await store.readTagSnapshot(null)).toMatchObject({
    status: "fresh",
    etag: null,
  });
});

test("reports an absent snapshot as unusable rather than as an empty clock", async () => {
  const missing = Object.assign(new Error("nope"), { name: "NoSuchKey" });
  const { store } = await storeWithObjects([missing]);

  expect(await store.readTagSnapshot(null)).toEqual({ status: "unusable" });
});

test("reports a snapshot at an unknown version as unusable", async () => {
  const { store } = await storeWithObjects([
    storedSnapshot({ ...snapshot, version: 2 }, '"v1"'),
  ]);

  expect(await store.readTagSnapshot(null)).toEqual({ status: "unusable" });
});

test("reports a snapshot document of null as unusable", async () => {
  const { store } = await storeWithObjects([storedSnapshot(null, '"v1"')]);

  expect(await store.readTagSnapshot(null)).toEqual({ status: "unusable" });
});

test("reads a snapshot with no records as a fresh, empty clock", async () => {
  const { store } = await storeWithObjects([
    storedSnapshot({ ...snapshot, records: {} }, '"v1"'),
  ]);

  expect(await store.readTagSnapshot(null)).toEqual({
    status: "fresh",
    records: {},
    etag: '"v1"',
  });
});

test("reports an unparseable snapshot as unusable", async () => {
  const { store } = await storeWithObjects([
    { Body: { transformToString: async () => "{" }, ETag: '"v1"' },
  ]);

  expect(await store.readTagSnapshot(null)).toEqual({ status: "unusable" });
});

test("surfaces a snapshot read failure that is neither a 404 nor a 304", async () => {
  const { store } = await storeWithObjects([new Error("s3 is down")]);

  await expect(store.readTagSnapshot(null)).rejects.toThrow(/down/);
});
