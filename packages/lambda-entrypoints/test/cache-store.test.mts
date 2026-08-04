import { afterEach, beforeEach, expect, test, vi } from "vitest";
import type { CacheStore } from "../src/next/cache-store.mjs";

// What the membrane injects when this substrate's edge offered a cache store.
// All five are set together or none is.
const storeEnv = {
  OCEL_ISR_STORE_BUCKET: "isr",
};

const adoptStore = () => Object.assign(process.env, storeEnv);

// The store binds its clients from env at construction, so every test needs the
// bucket, prefix and namespace it keys into. The adopted-store vars start
// absent: a test that wants one opts in.
beforeEach(() => {
  process.env.OCEL_ISR_BUCKET = "assets";
  process.env.OCEL_ISR_PREFIX = "prod/proj/app/BID";
  process.env.OCEL_STATE_TABLE = "state";
  process.env.OCEL_ISR_TAG_NAMESPACE = "TAG#prod#proj#app#BID#";
  for (const name of Object.keys(storeEnv)) delete process.env[name];
  // Likewise opt-in: absent, entry writes go straight to the object store.
  delete process.env.OCEL_ISR_WRITER_URL;
  delete process.env.OCEL_ISR_WRITER_SECRET;
});

// What the deploy injects when this substrate adopted an ISR writer.
const adoptWriter = () =>
  Object.assign(process.env, {
    OCEL_ISR_WRITER_URL: "https://writer.example/prod/proj/app/BID/entry",
    OCEL_ISR_WRITER_SECRET: "write-secret",
  });

afterEach(async () => {
  // The tag clock lives on globalThis so the two handler bundles share one, so
  // resetting modules does not reset it: a store bound to a mock this test built
  // would answer the next test's reads, and its sync throttle would suppress
  // them. Unbound before the registry is reset, or importing it here would
  // repopulate the registry with an unmocked graph for the next test to find.
  (await import("../src/next/tag-clock.mjs")).setTagClockStore(null);
  vi.useRealTimers();
  vi.resetModules();
  vi.restoreAllMocks();
  // A stubbed fetch is the writer worker standing in for itself, and it would
  // otherwise stand in for the next test's too.
  vi.unstubAllGlobals();
});

const TABLE = "state";

// Drives the store against a scripted DynamoDB: each entry is one send()
// response, in order. A response that is an Error is thrown instead.
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
  const { awsCacheStore } = await import("../src/next/cache-store.mjs");
  return { store: awsCacheStore(), built, sent, ddbSent };
}

// Stubs the writer worker, capturing what it was asked for.
function writerAnswering(...responses: Response[]) {
  const calls: Array<[string, any]> = [];
  vi.stubGlobal("fetch", async (url: any, init: any) => {
    calls.push([String(url), init ?? {}]);
    return responses.shift() ?? new Response(null, { status: 204 });
  });
  return calls;
}

// Entry reads travel through the writer exactly as writes do, and that is the
// whole credential-hygiene argument: with both halves on the far side of the
// worker, this function holds no R2 credential for entries at all. A read left
// direct would have kept a bucket-wide R2 token here, since R2 tokens scope to a
// bucket and have no key-prefix grammar — so nothing would have been won.
test("routes entry reads through the ISR writer when one is injected", async () => {
  adoptStore();
  adoptWriter();
  const calls = writerAnswering(
    new Response(`{"lastModified":1,"value":{}}`, { status: 200 }),
  );

  const { store, built, sent } = await entryStore();

  expect(await store.readEntry("index")).toEqual({ lastModified: 1, value: {} });
  expect(calls).toHaveLength(1);
  expect(calls[0][0]).toBe("https://writer.example/prod/proj/app/BID/entry?key=index");
  expect(calls[0][1].headers.authorization).toBe("Bearer write-secret");
  // No S3 client is ever built against the adopted store's injected keys, and
  // nothing is sent to one.
  expect(built.some((cfg) => cfg.credentials !== undefined)).toBe(false);
  expect(sent).toHaveLength(0);
});

// The writer worker puts into the adopted store and nowhere else, and the two
// are adopted from separate parameters. A deploy holding one without the other
// would write into the edge's bucket and read from the provider's — a miss on
// every request, a re-render on every miss, and no signal anywhere. It is a
// broken deploy, so it says so at the first thing that asks for a store.
test("refuses to run with an adopted store and no writer to write into it", async () => {
  adoptStore();

  await expect(entryStore()).rejects.toThrow("OCEL_ISR_WRITER_URL");
});

// One key spelling for both ops, so the worker derives one object key whichever
// asked. A read that named the key differently from the write would find nothing
// the origin had just cached: a miss on every request, forever, silently.
test("names an entry the same way on the read as on the write", async () => {
  adoptStore();
  adoptWriter();
  const calls = writerAnswering(
    new Response(`{"lastModified":1,"value":{}}`, { status: 200 }),
    new Response(null, { status: 204 }),
  );

  const { store } = await entryStore();
  // `trailingSlash: true` produces this key on every route, and it is the one
  // the two grammars parted company over before.
  await store.readEntry("blog/");
  await store.writeEntry("blog/", { lastModified: 1, value: {} });

  expect(calls[0][0]).toBe(calls[1][0]);
  expect(calls[0][0]).toBe("https://writer.example/prod/proj/app/BID/entry?key=blog%2F");
});

// An entry read is on the serving path: Next calls get() for every request to a
// cached route, and does not wrap it. So a writer that is down, slow or refusing
// has to read as a miss — which makes Next render — rather than as an error,
// which would make the request fail. A writer outage is slow, never broken.
test.each([
  ["unreachable", () => Promise.reject(new TypeError("fetch failed"))],
  ["timing out", () => Promise.reject(Object.assign(new Error("aborted"), { name: "TimeoutError" }))],
  ["erroring", () => Promise.resolve(new Response("Internal Error", { status: 503 }))],
  ["refusing the credential", () => Promise.resolve(new Response("Unauthorized", { status: 401 }))],
  ["refusing the key", () => Promise.resolve(new Response("Bad Request", { status: 400 }))],
])("serves a cache miss when the writer is %s", async (_name, outcome) => {
  adoptStore();
  adoptWriter();
  vi.spyOn(console, "warn").mockImplementation(() => {});
  vi.stubGlobal("fetch", outcome);

  const { store } = await entryStore();

  await expect(store.readEntry("blog/post")).resolves.toBeNull();
  // Including the key the writer would refuse outright: unaddressable is a miss
  // on this side of the boundary too, never a throw into the render.
  await expect(store.readEntry("../other/index")).resolves.toBeNull();
});

// The unadopted path keeps its own grammar check, since nothing downstream of it
// applies one: a key that cannot be addressed inside the prefix is a caller bug.
test("refuses a key that would address something outside the deploy's prefix", async () => {
  const { store } = await entryStore();

  await expect(store.readEntry("../other/index")).rejects.toThrow("not addressable");
});

// Fetch entries hold upstream response bodies — origin-private data that must
// not follow route entries into a third party's store. Adoption moves routes to
// the edge and leaves fetch behind, so this asserts the one case where the two
// backends diverge: same prefix, different bucket, different credentials.
test("keeps fetch entries on the provider's bucket even when a store is adopted", async () => {
  adoptStore();
  adoptWriter();
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
  // The only client this store builds, on the function's own role rather than
  // any injected keys — adopting a store no longer builds one at all.
  expect(built).toHaveLength(1);
  expect(built[0].endpoint).toBeUndefined();
  expect(built[0].credentials).toBeUndefined();
});

test("writes fetch entries to the provider's bucket under the fetch prefix", async () => {
  adoptStore();
  adoptWriter();
  const { store, sent } = await entryStore();

  await store.writeFetch("deadbeef", { lastModified: 1, value: {} });

  expect(sent[0]).toMatchObject({
    Bucket: "assets",
    Key: "prod/proj/app/BID/fetch-cache/deadbeef.cache.json",
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

// The read path's whole tag check, against the real store rather than a fake:
// one GET of this build's snapshot, and nothing sent to the table at all. What
// it replaces is a BatchGetItem per request per tagged route, whose failure mode
// was a miss and therefore a re-render — so under a throttled table every
// tagged route in the fleet re-rendered at once, which is the herd this epic
// exists to remove. A read reappearing here is silent: it would serve correctly
// and only cost.
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
  const clock = await import("../src/next/tag-clock.mjs");
  const { awsUseCacheStore } = await import("../src/next/use-cache-store.mjs");
  clock.setTagClockStore(awsUseCacheStore());

  const entry = await (await handlerOver(store)).get("/", { kind: "APP_PAGE" });

  // The tag's expiry predates the entry, so it cannot expire it — and the only
  // way to know that was read out of the snapshot object.
  expect(entry).not.toBeNull();
  expect(sent.map((s: any) => s.Key)).toEqual([
    "prod/proj/app/BID/cache/index.cache.json",
    "prod/proj/app/BID/tag-clock.json",
  ]);
  expect(ddbSent).toEqual([]);
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

// Enough of UpdateItem's SET and guard for a write by either store to be
// inspected as the item it actually left on the table — which is the record the
// stream hands the publisher.
function fakeTable() {
  const items = new Map<string, Record<string, any>>();

  const send = (input: any) => {
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
  return { send, items };
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
          return table.send(cmd.input);
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
  return { store: awsCacheStore(), useStore: awsUseCacheStore(), items: table.items };
}

// The classic ISR model registers only the singular handler and has no `use
// cache` anywhere, so nothing else writes its tags. Its invalidations have to
// reach the stream's index attributes on their own — the publisher derives the
// build it publishes for from gsi1pk, so an item written without them is an
// invalidation no other instance ever hears about.
test("makes a classic-model invalidation visible to the stream publisher", async () => {
  const { store, items } = await storesOverTable();

  await (await handlerOver(store)).revalidateTag("products");

  const item = items.get("TAG#prod#proj#app#BID#products")!;
  expect(item.tag.S).toBe("products");
  expect(item.gsi1pk.S).toBe("TAG#prod#proj#app#BID#");
  expect(Number(item.expired.N)).toBeGreaterThan(0);
  expect(Number(item.gsi1sk.S)).toBeGreaterThan(0);
});

// The two tiers advance one shared watermark, which is only safe while every
// writer agrees the guard is monotonic.
test("an older singular write cannot walk back a newer plural one", async () => {
  const { store, useStore, items } = await storesOverTable();

  await useStore.writeTag("products", { expired: 2_000, writtenAt: 2_000 });
  await store.writeTags(["products"], { expired: 1_000 });

  const item = items.get("TAG#prod#proj#app#BID#products")!;
  expect(item.expired.N).toBe("2000");
  expect(Number(item.gsi1sk.S)).toBe(2_000);
});

// With a writer adopted, a route entry leaves through it and never through the
// object-store client — which is the whole credential-hygiene argument: the
// function no longer needs a standing write credential for entries.
test("routes entry writes through the ISR writer when one is injected", async () => {
  adoptStore();
  adoptWriter();
  const fetches: Array<[string, any]> = [];
  vi.stubGlobal("fetch", async (url: any, init: any) => {
    fetches.push([String(url), init]);
    return new Response(null, { status: 204 });
  });

  const { store, sent } = await entryStore();
  await store.writeEntry("blog/post", { lastModified: 1, value: {} });

  expect(fetches).toHaveLength(1);
  expect(fetches[0][0]).toBe(
    "https://writer.example/prod/proj/app/BID/entry?key=blog%2Fpost",
  );
  expect(sent).toHaveLength(0);
});

// Fetch entries carry origin-private upstream bodies and must never reach the
// edge's store, writer or not.
test("keeps fetch entries off the ISR writer", async () => {
  adoptStore();
  adoptWriter();
  const fetches: string[] = [];
  vi.stubGlobal("fetch", async (url: any) => {
    fetches.push(String(url));
    return new Response(null, { status: 204 });
  });

  const { store, sent } = await entryStore();
  await store.writeFetch("deadbeef", { lastModified: 1, value: {} });

  expect(fetches).toHaveLength(0);
  expect(sent[0]).toMatchObject({
    Bucket: "assets",
    Key: "prod/proj/app/BID/fetch-cache/deadbeef.cache.json",
  });
});
