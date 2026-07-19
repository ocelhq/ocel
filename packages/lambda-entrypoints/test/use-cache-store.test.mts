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
  // Whether a store was adopted decides which bucket the store binds, so it has
  // to be cleared between tests or one test's adoption leaks into the next.
  for (const v of Object.keys(process.env)) {
    if (v.startsWith("OCEL_ISR_STORE_")) delete process.env[v];
  }
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

// The plural handlers run in Lambda, where the provider's own bucket is
// in-region, so adopting an edge cache store deliberately does not move them:
// only the singular ISR entry path is colocated with the edge.
test("stays on the provider's bucket when a cache store is adopted", async () => {
  Object.assign(process.env, {
    OCEL_ISR_STORE_BUCKET: "isr",
    OCEL_ISR_STORE_ENDPOINT: "https://acct.r2.cloudflarestorage.com",
    OCEL_ISR_STORE_REGION: "auto",
    OCEL_ISR_STORE_ACCESS_KEY_ID: "AK",
    OCEL_ISR_STORE_SECRET_ACCESS_KEY: "s3cret",
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

// A stale-only event carries no expiry, and writing one as 0 would both clobber
// an expiry another instance set and — the guard being a strict `<` — wedge the
// record, so every later stale-only write is rejected against its own zero.
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

// performance.now() is fractional, and a fractional sort key neither pads to the
// fixed width nor orders lexicographically against one that does.
test("rounds a fractional write time into the fixed-width sort key", async () => {
  const { store, sends } = await storeWithResponses([{}]);

  await store.writeTag("products", { expired: 9, writtenAt: 1700.6 });

  expect(sends[0].ExpressionAttributeValues[":writtenAt"]).toEqual({
    S: "000000000001701",
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
    // Inclusive: a truncated page advances the cursor only to the last record
    // consumed, so a strict `>` would skip any record sharing that millisecond.
    KeyConditionExpression: "gsi1pk = :ns AND gsi1sk >= :since",
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

// The tag snapshot is the edge's replica of the clock. It lives in the adopted
// store because that is the one the edge can read, and is written only under a
// conditional PUT so two publishers racing cannot lose an invalidation.
const adopted = {
  OCEL_ISR_STORE_BUCKET: "isr",
  OCEL_ISR_STORE_ENDPOINT: "https://acct.r2.cloudflarestorage.com",
  OCEL_ISR_STORE_REGION: "auto",
  OCEL_ISR_STORE_ACCESS_KEY_ID: "AK",
  OCEL_ISR_STORE_SECRET_ACCESS_KEY: "s3cret",
};

const snapshot = {
  version: 1 as const,
  deployedAt: 1_000,
  generatedAt: 2_000,
  validUntil: 3_000,
  records: { products: { expired: 1_500 } },
};

async function snapshotsWith(responses: any[], env: Record<string, string> = adopted) {
  Object.assign(process.env, env);
  const { store, sends } = await storeWithObjects(responses);
  return { snapshots: store.snapshots, sends };
}

test("keys the snapshot beside the build's cache entries in the adopted store", async () => {
  const { snapshots, sends } = await snapshotsWith([{}]);

  await snapshots!.write(snapshot, null);

  expect(sends[0].Bucket).toBe("isr");
  expect(sends[0].Key).toBe("prod/proj/app/BID/tag-clock.json");
  expect(sends[0].ContentType).toBe("application/json");
  expect(JSON.parse(sends[0].Body)).toEqual(snapshot);
});

// Creating is conditional too, so a publisher that read "absent" cannot clobber
// an object another publisher — or the deploy's own seed — created meanwhile.
test("creates the snapshot only where none exists", async () => {
  const { snapshots, sends } = await snapshotsWith([{}]);

  await expect(snapshots!.write(snapshot, null)).resolves.toBe(true);

  expect(sends[0].IfNoneMatch).toBe("*");
  expect(sends[0].IfMatch).toBeUndefined();
});

test("replaces the snapshot only where it still carries the etag that was read", async () => {
  const { snapshots, sends } = await snapshotsWith([{}]);

  await expect(snapshots!.write(snapshot, { snapshot, etag: '"abc"' })).resolves.toBe(
    true,
  );

  expect(sends[0].IfMatch).toBe('"abc"');
  expect(sends[0].IfNoneMatch).toBeUndefined();
});

// An object the store named no version for can satisfy neither precondition, so
// conditioning on one would fail every publish for the life of the build. The
// compare-and-swap is what is given up, and the monotonic merge plus the next
// publish are what make that bounded.
test("replaces an unversioned snapshot unconditionally", async () => {
  const { snapshots, sends } = await snapshotsWith([{}]);

  await expect(snapshots!.write(snapshot, { snapshot, etag: null })).resolves.toBe(true);

  expect(sends[0].IfMatch).toBeUndefined();
  expect(sends[0].IfNoneMatch).toBeUndefined();
});

// Two publishers racing is the ordinary path, not an error: the loser re-reads
// the winner's snapshot and merges onto it.
test("reports a failed precondition rather than throwing", async () => {
  const rejected = Object.assign(new Error("precondition"), {
    $metadata: { httpStatusCode: 412 },
  });
  const { snapshots } = await snapshotsWith([rejected]);

  await expect(snapshots!.write(snapshot, { snapshot, etag: '"abc"' })).resolves.toBe(
    false,
  );
});

// A lost grant must not read as "someone else won", which would leave the
// replica permanently and invisibly stale.
test("surfaces a write failure that is not a failed precondition", async () => {
  const denied = Object.assign(new Error("denied"), {
    $metadata: { httpStatusCode: 403 },
  });
  const { snapshots } = await snapshotsWith([denied]);

  await expect(snapshots!.write(snapshot, { snapshot, etag: '"abc"' })).rejects.toThrow(
    /denied/,
  );
});

test("reads the snapshot back with the etag the next write conditions on", async () => {
  const { snapshots } = await snapshotsWith([
    { ETag: '"abc"', Body: { transformToString: async () => JSON.stringify(snapshot) } },
  ]);

  await expect(snapshots!.read()).resolves.toEqual({ snapshot, etag: '"abc"' });
});

// deployedAt is written only by the deploy's genesis seed, so a read that
// reported a torn blob as usable-but-empty would have the publisher overwrite it
// with a zero anchor and disable pruning for the life of the build.
test("surfaces a torn snapshot rather than reporting it absent or empty", async () => {
  const { snapshots } = await snapshotsWith([
    { ETag: '"abc"', Body: { transformToString: async () => "{not json" } },
  ]);

  await expect(snapshots!.read()).rejects.toThrow();
});

// A missing etag is not an absent object: reporting it as one makes the next
// write condition on "create", which 412s against the object that is already
// there — every time, for the life of the build.
test("reads an existing object the store gave no etag for", async () => {
  const { snapshots } = await snapshotsWith([
    { Body: { transformToString: async () => JSON.stringify(snapshot) } },
  ]);

  await expect(snapshots!.read()).resolves.toEqual({ snapshot, etag: null });
});

test("reports an absent snapshot as nothing to merge onto", async () => {
  const missing = Object.assign(new Error("nope"), { name: "NoSuchKey" });
  const { snapshots } = await snapshotsWith([missing]);

  await expect(snapshots!.read()).resolves.toBeNull();
});

// No adopted store means no edge reading a replica, so there is nothing to
// publish and the clock behaves exactly as it did before replication existed.
test("has no snapshot store when the substrate adopted none", async () => {
  const { snapshots } = await snapshotsWith([], {});

  expect(snapshots).toBeNull();
});
