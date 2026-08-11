import { afterEach, beforeEach, expect, test, vi } from "vitest";
import type { CacheStore } from "../src/next/cache-store.mjs";

const storeEnv = {
  OCEL_ISR_STORE_BUCKET: "isr",
};

const adoptStore = () => Object.assign(process.env, storeEnv);

beforeEach(() => {
  process.env.OCEL_ISR_BUCKET = "assets";
  process.env.OCEL_ISR_PREFIX = "prod/proj/app/BID";
  process.env.OCEL_STATE_TABLE = "state";
  process.env.OCEL_ISR_TAG_NAMESPACE = "PROJECT#proj#STACK#prod--app--r3f8a1c9d#TAG#";
  for (const name of Object.keys(storeEnv)) delete process.env[name];
  delete process.env.OCEL_ISR_WRITER_URL;
  delete process.env.OCEL_ISR_WRITER_SECRET;
});

const adoptWriter = () =>
  Object.assign(process.env, {
    OCEL_ISR_WRITER_URL: "https://writer.example/prod/proj/app/BID/entry",
    OCEL_ISR_WRITER_SECRET: "write-secret",
  });

afterEach(async () => {
  (await import("../src/next/tag-clock.mjs")).setTagClockStore(null);
  vi.useRealTimers();
  vi.resetModules();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

const TABLE = "state";

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

async function handlerOver(store: CacheStore) {
  const { default: OcelCacheHandler } = await import("../src/next/cache-handler.mjs");
  OcelCacheHandler.store = store;
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
  const { awsCacheStore } = await import("../src/next/cache-store.mjs");
  return { store: awsCacheStore(), built, sent, ddbSent };
}

function writerAnswering(...responses: Response[]) {
  const calls: Array<[string, any]> = [];
  vi.stubGlobal("fetch", async (url: any, init: any) => {
    calls.push([String(url), init ?? {}]);
    return responses.shift() ?? new Response(null, { status: 204 });
  });
  return calls;
}

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
  expect(built.some((cfg) => cfg.credentials !== undefined)).toBe(false);
  expect(sent).toHaveLength(0);
});

test("refuses to run with an adopted store and no writer to write into it", async () => {
  adoptStore();

  await expect(entryStore()).rejects.toThrow("OCEL_ISR_WRITER_URL");
});

test("names an entry the same way on the read as on the write", async () => {
  adoptStore();
  adoptWriter();
  const calls = writerAnswering(
    new Response(`{"lastModified":1,"value":{}}`, { status: 200 }),
    new Response(null, { status: 204 }),
  );

  const { store } = await entryStore();
  await store.readEntry("blog/");
  await store.writeEntry("blog/", { lastModified: 1, value: {} });

  expect(calls[0][0]).toBe(calls[1][0]);
  expect(calls[0][0]).toBe("https://writer.example/prod/proj/app/BID/entry?key=blog%2F");
});

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
  await expect(store.readEntry("../other/index")).resolves.toBeNull();
});

test("refuses a key that would address something outside the deploy's prefix", async () => {
  const { store } = await entryStore();

  await expect(store.readEntry("../other/index")).rejects.toThrow("not addressable");
});

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
    UpdateExpression:
      "SET tag = :tag, gsi1pk = :ns, gsi1sk = :writtenAt, expired = :expired",
    ExpressionAttributeValues: {
      ":tag": { S: "products" },
      ":ns": { S: "PROJECT#proj#STACK#prod--app--r3f8a1c9d#TAG#" },
      ":writtenAt": { S: "000000000001700" },
      ":expired": { N: "1700" },
    },
  });
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
