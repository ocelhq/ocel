import { createExecutionContext, env } from "cloudflare:test";
import {
  type TagRecord,
  type TagSnapshot,
  tagNamespace,
  tagSnapshotKey,
} from "@framework/next-cache";
import { beforeEach, expect, it } from "vitest";

import {
  CacheEntrypoint,
  createEdgeCache,
  type FetchEntryStore,
  tagRaiser,
} from "../src/cache-entrypoint";
import type { Env } from "../src/index";
import type { AwsService } from "../src/signing";
import type { ObjectStoreReader } from "../src/tag-clock";

declare module "cloudflare:test" {
  interface ProvidedEnv {
    TAG_SNAPSHOT_STORE: R2Bucket;
  }
}

const region = "eu-west-2";
const table = "ocel-state";

function snapshotBucket(): ObjectStoreReader {
  return { get: (key) => env.TAG_SNAPSHOT_STORE.get(key) };
}

function raiseRecorder(
  land: (records: Record<string, TagRecord>) => Promise<void> = async () => {},
) {
  const raises: { scope: string; records: Record<string, TagRecord> }[] = [];
  return {
    raises,
    raise: async (scope: string, records: Record<string, TagRecord>) => {
      raises.push({ scope, records });
      await land(records);
    },
  };
}

interface Call {
  service: AwsService;
  url: string;
  method: string;
  headers: Headers;
  body: string;
}

function awsRecorder(
  reply: (call: Call) => Response | Promise<Response> = () => new Response(null, { status: 404 }),
) {
  const calls: Call[] = [];
  return {
    calls,
    send: async (service: AwsService, url: string, init?: RequestInit) => {
      const call: Call = {
        service,
        url,
        method: init?.method ?? "GET",
        headers: new Headers(init?.headers),
        body:
          typeof init?.body === "string"
            ? init.body
            : init?.body
              ? new TextDecoder().decode(init.body as Uint8Array)
              : "",
      };
      calls.push(call);
      return reply(call);
    },
  };
}

function waitUntilRecorder() {
  const pending: Promise<unknown>[] = [];
  return { pending, waitUntil: (promise: Promise<unknown>) => void pending.push(promise) };
}

const entry = (over: Record<string, unknown> = {}) => ({
  lastModified: 1_000,
  value: { kind: "FETCH", data: { body: "hi" }, ...over },
});

let scopes = 0;
let scope = "";
beforeEach(() => {
  scope = `prod/proj/app/r${String(scopes++).padStart(8, "0")}/isr`;
});

async function seedSnapshot(records: TagSnapshot["records"], deployedAt = 0): Promise<void> {
  await env.TAG_SNAPSHOT_STORE.put(
    tagSnapshotKey(scope),
    JSON.stringify({ version: 1, deployedAt, generatedAt: 0, records }),
  );
}

const entryKey = (key: string, prefix = scope) => `${prefix}/fetch-cache/${key}.cache.json`;

async function seedEntry(value: unknown, key = "abc123"): Promise<void> {
  await env.TAG_SNAPSHOT_STORE.put(entryKey(key), JSON.stringify(value));
}

async function storedEntry(key = "abc123"): Promise<unknown> {
  return (await env.TAG_SNAPSHOT_STORE.get(entryKey(key)))?.json();
}

function entryStore(over: Partial<FetchEntryStore> = {}) {
  const puts: { key: string; contentType?: string }[] = [];
  const store: FetchEntryStore = {
    get: (key) => env.TAG_SNAPSHOT_STORE.get(key),
    put: async (key, value, options) => {
      puts.push({ key, contentType: options?.httpMetadata?.contentType });
      return env.TAG_SNAPSHOT_STORE.put(key, value, options);
    },
    ...over,
  };
  return { puts, store };
}

function cacheWith(
  over: {
    scope?: string;
    aws?: ReturnType<typeof awsRecorder>;
    entries?: FetchEntryStore;
    store?: ObjectStoreReader;
    raise?: (scope: string, records: Record<string, TagRecord>) => Promise<void>;
    waitUntil?: (p: Promise<unknown>) => void;
    now?: () => number;
  } = {},
) {
  return createEdgeCache({
    scope: over.scope ?? scope,
    entries: over.entries ?? entryStore().store,
    tagTable: over.aws ? { region, table, aws: over.aws.send } : undefined,
    snapshots: over.store ?? snapshotBucket(),
    raise: over.raise ?? raiseRecorder().raise,
    waitUntil: over.waitUntil ?? (() => {}),
    now: over.now ?? (() => 5_000),
  });
}

it("reads a fetch entry from the cache store under the deployment's prefix", async () => {
  const stored = entry();
  await seedEntry(stored);
  const aws = awsRecorder();

  expect(await cacheWith({ aws }).fetchGet(scope, "abc123", [])).toEqual(stored);
  expect(aws.calls).toHaveLength(0);
});

it("misses on an absent object", async () => {
  expect(await cacheWith().fetchGet(scope, "abc123", [])).toBeNull();
});

it("misses rather than throwing when the store is unreachable", async () => {
  const { store } = entryStore({
    get: async () => {
      throw new Error("r2 down");
    },
  });
  expect(await cacheWith({ entries: store }).fetchGet(scope, "abc123", [])).toBeNull();
});

it("misses when a tag was invalidated after the entry was written", async () => {
  await seedSnapshot({ posts: { expired: 2_000 } });
  await seedEntry(entry());
  expect(await cacheWith().fetchGet(scope, "abc123", ["posts"])).toBeNull();
});

it("serves an entry whose tags are clean", async () => {
  await seedSnapshot({ posts: { expired: 500 } });
  await seedEntry(entry());
  expect(await cacheWith().fetchGet(scope, "abc123", ["posts"])).toEqual(entry());
});

it("evaluates the tags the entry itself recorded, not only the caller's", async () => {
  await seedSnapshot({ authors: { expired: 2_000 } });
  await seedEntry(entry({ tags: ["authors"] }));
  expect(await cacheWith().fetchGet(scope, "abc123", ["posts"])).toBeNull();
});

it("misses when the tag snapshot cannot be trusted", async () => {
  await seedEntry(entry());
  expect(await cacheWith().fetchGet(scope, "abc123", ["posts"])).toBeNull();
  expect(await cacheWith().fetchGet(scope, "abc123", [])).toEqual(entry());
});

it("returns from a write before the object lands, and writes behind it", async () => {
  let release = () => {};
  const blocked = new Promise<void>((resolve) => (release = resolve));
  const { puts, store } = entryStore();
  const entries: FetchEntryStore = {
    ...store,
    put: async (key, value, options) => {
      await blocked;
      return store.put(key, value, options);
    },
  };
  const { pending, waitUntil } = waitUntilRecorder();

  await cacheWith({ entries, waitUntil }).fetchSet(scope, "abc123", entry(), ["posts"]);

  expect(pending).toHaveLength(1);
  expect(await storedEntry()).toBeUndefined();
  expect(puts).toHaveLength(0);
  release();
  await Promise.all(pending);

  expect(puts).toEqual([{ key: entryKey("abc123"), contentType: "application/json" }]);
  expect(await storedEntry()).toEqual({
    ...entry(),
    value: { ...entry().value, tags: ["posts"] },
  });
});

it("skips an oversized entry rather than failing the render", async () => {
  const { puts, store } = entryStore();
  const { pending, waitUntil } = waitUntilRecorder();
  const huge = entry({ data: { body: "x".repeat(3 * 1024 * 1024) } });

  await expect(
    cacheWith({ entries: store, waitUntil }).fetchSet(scope, "abc123", huge, []),
  ).resolves.toBeUndefined();
  expect(puts).toHaveLength(0);
  expect(pending).toHaveLength(0);
});

it.each(["APP_PAGE", "APP_ROUTE", "PAGES", undefined])(
  "refuses to store a %s entry rather than corrupting the fetch cache",
  async (kind) => {
    const { puts, store } = entryStore();
    const { pending, waitUntil } = waitUntilRecorder();

    await expect(
      cacheWith({ entries: store, waitUntil }).fetchSet(scope, "abc123", entry({ kind }), []),
    ).rejects.toThrow(/fetch entries only/);
    expect(puts).toHaveLength(0);
    expect(pending).toHaveLength(0);
  },
);

it("does not surface a failed write to the caller", async () => {
  const { store } = entryStore({
    put: async () => {
      throw new Error("429 Too Many Requests");
    },
  });
  const { pending, waitUntil } = waitUntilRecorder();
  await cacheWith({ entries: store, waitUntil }).fetchSet(scope, "abc123", entry(), []);
  await expect(Promise.all(pending)).resolves.toBeDefined();
});

it("records one tag update per tag, under the prefix's tag namespace", async () => {
  const aws = awsRecorder(() => new Response("{}"));
  await cacheWith({ aws }).revalidateTags(scope, ["posts", "authors"]);

  const updates = aws.calls.filter((call) => call.service === "dynamodb");
  expect(updates).toHaveLength(2);
  expect(updates[0].url).toBe(`https://dynamodb.${region}.amazonaws.com/`);
  expect(updates[0].headers.get("x-amz-target")).toBe("DynamoDB_20120810.UpdateItem");
  const body = JSON.parse(updates[0].body);
  expect(body.TableName).toBe(table);
  expect(body.Key.pk.S).toBe(`${tagNamespace(scope)}posts`);
  expect(body.ExpressionAttributeValues[":expired"].N).toBe("5000");
});

it("writes no tag item at all for a scope that is not one release's ISR prefix", async () => {
  const aws = awsRecorder(() => new Response("{}"));
  const bound = "prod/proj/app/r00000000";
  await expect(cacheWith({ aws, scope: bound }).revalidateTags(bound, ["posts"])).rejects.toThrow(
    /ISR prefix/,
  );
  expect(aws.calls).toHaveLength(0);
});

it("marks tags stale now and dead at the end of an expire window", async () => {
  const aws = awsRecorder(() => new Response("{}"));
  await cacheWith({ aws }).revalidateTags(scope, ["posts"], { expire: 60 });

  const values = JSON.parse(aws.calls[0].body).ExpressionAttributeValues;
  expect(values[":stale"].N).toBe("5000");
  expect(values[":expired"].N).toBe(String(5_000 + 60_000));
});

it("treats a rejected guard as the ordinary outcome it is", async () => {
  const writer = raiseRecorder();
  const aws = awsRecorder((call) =>
    call.service === "dynamodb"
      ? new Response(
          JSON.stringify({
            __type: "com.amazonaws.dynamodb.v20120810#ConditionalCheckFailedException",
          }),
          { status: 400 },
        )
      : new Response("{}"),
  );
  await expect(
    cacheWith({ aws, raise: writer.raise }).revalidateTags(scope, ["posts"]),
  ).resolves.toBeUndefined();
  expect(writer.raises).toHaveLength(1);
});

it("surfaces a tag write that failed for any other reason", async () => {
  const aws = awsRecorder(
    () => new Response(JSON.stringify({ __type: "x#ThrottlingException" }), { status: 400 }),
  );
  await expect(cacheWith({ aws }).revalidateTags(scope, ["posts"])).rejects.toThrow(/dynamodb 400/);
});

it("raises every invalidated tag through the writer, under this deployment's prefix", async () => {
  const writer = raiseRecorder();
  const aws = awsRecorder(() => new Response("{}"));
  await cacheWith({ aws, raise: writer.raise }).revalidateTags(scope, ["posts", "authors"], {
    expire: 60,
  });

  expect(writer.raises).toEqual([
    {
      scope,
      records: {
        posts: { stale: 5_000, expired: 65_000 },
        authors: { stale: 5_000, expired: 65_000 },
      },
    },
  ]);
});

it("records the invalidation durably before raising it", async () => {
  const order: string[] = [];
  const aws = awsRecorder(() => {
    order.push("dynamodb");
    return new Response("{}");
  });
  await cacheWith({ aws, raise: async () => void order.push("raise") }).revalidateTags(scope, [
    "posts",
  ]);

  expect(order).toEqual(["dynamodb", "raise"]);
});

it("does not fail the route when the writer refuses the raise", async () => {
  const aws = awsRecorder(() => new Response("{}"));

  await expect(
    cacheWith({
      aws,
      raise: async () => {
        throw new Error("429");
      },
    }).revalidateTags(scope, ["posts"]),
  ).resolves.toBeUndefined();

  expect(aws.calls.filter((call) => call.service === "dynamodb")).toHaveLength(1);
});

it("sees its own invalidation on the very next read", async () => {
  const stored = entry();
  await seedEntry(stored);
  const aws = awsRecorder(() => new Response("{}"));
  const writer = raiseRecorder((records) => seedSnapshot(records));
  const cache = cacheWith({ aws, store: snapshotBucket(), raise: writer.raise });

  await seedSnapshot({ posts: { expired: 500 } });
  expect(await cache.fetchGet(scope, "abc123", ["posts"])).toEqual(stored);

  await cache.revalidateTags(scope, ["posts"]);
  expect(await cache.fetchGet(scope, "abc123", ["posts"])).toBeNull();
});

it("drops its snapshot memo even when the raise failed", async () => {
  const stored = entry();
  await seedEntry(stored);
  const aws = awsRecorder(() => new Response("{}"));
  const cache = cacheWith({
    aws,
    store: snapshotBucket(),
    raise: async () => {
      throw new Error("429");
    },
  });

  await seedSnapshot({ posts: { expired: 500 } });
  expect(await cache.fetchGet(scope, "abc123", ["posts"])).toEqual(stored);

  await cache.revalidateTags(scope, ["posts"]);
  await seedSnapshot({ posts: { expired: 5_000 } });
  expect(await cache.fetchGet(scope, "abc123", ["posts"])).toBeNull();
});

it("posts a raise to the writer under this deploy's own write secret", async () => {
  const posted: Request[] = [];
  const raise = tagRaiser(
    {
      fetch: async (request: Request) => {
        posted.push(request);
        return new Response(null, { status: 204 });
      },
    },
    "write-secret",
  );

  await raise(scope, { posts: { expired: 5_000 } });

  expect(posted).toHaveLength(1);
  expect(new URL(posted[0].url).pathname).toBe(`/${scope}/tags`);
  expect(posted[0].method).toBe("POST");
  expect(posted[0].headers.get("authorization")).toBe("Bearer write-secret");
  expect(await posted[0].json()).toEqual({ records: { posts: { expired: 5_000 } } });
});

it.each([429, 401, 500])("reports a raise the writer answered with %i", async (status) => {
  const raise = tagRaiser({ fetch: async () => new Response(null, { status }) }, "write-secret");
  await expect(raise(scope, { posts: { expired: 5_000 } })).rejects.toThrow(String(status));
});

it("reports a raise it has no writer to make", async () => {
  await expect(tagRaiser(undefined, "write-secret")(scope, {})).rejects.toThrow(/no isr writer/);
  await expect(
    tagRaiser({ fetch: async () => new Response(null) }, undefined)(scope, {}),
  ).rejects.toThrow(/no isr writer/);
});

const otherScope = "prod/proj/other/r00000000/isr";

it("answers no entry for a scope other than the one its deployment bound", async () => {
  await env.TAG_SNAPSHOT_STORE.put(entryKey("abc123", otherScope), JSON.stringify(entry()));
  const reads: string[] = [];
  const { store } = entryStore({
    get: async (key) => {
      reads.push(key);
      return env.TAG_SNAPSHOT_STORE.get(key);
    },
  });
  expect(await cacheWith({ entries: store }).fetchGet(otherScope, "abc123", [])).toBeNull();
  expect(reads).toHaveLength(0);
});

it("writes nothing for a scope other than the one its deployment bound", async () => {
  const { puts, store } = entryStore();
  const { pending, waitUntil } = waitUntilRecorder();

  await expect(
    cacheWith({ entries: store, waitUntil }).fetchSet(otherScope, "abc123", entry(), []),
  ).resolves.toBeUndefined();
  expect(puts).toHaveLength(0);
  expect(pending).toHaveLength(0);
});

it("refuses to invalidate tags of a scope other than the one its deployment bound", async () => {
  const writer = raiseRecorder();
  const aws = awsRecorder(() => new Response("{}"));

  await expect(
    cacheWith({ aws, raise: writer.raise }).revalidateTags(otherScope, ["posts"]),
  ).rejects.toThrow(`bound to ${scope}, not ${otherScope}`);
  expect(aws.calls).toHaveLength(0);
  expect(writer.raises).toHaveLength(0);
});

it("answers like an empty cache when its deployment bound no scope", async () => {
  const entrypoint = new CacheEntrypoint(createExecutionContext(), {
    OCEL_EDGE_ACCESS_KEY_ID: "id",
    OCEL_EDGE_SECRET_KEY: "secret",
    OCEL_AWS_REGION: region,
    OCEL_STATE_TABLE: table,
    OCEL_CACHE_STORE: env.TAG_SNAPSHOT_STORE,
  } as Env);

  expect(await entrypoint.fetchGet(scope, "abc123", [])).toBeNull();
  await entrypoint.fetchSet(scope, "abc123", entry(), []);
  await entrypoint.revalidateTags(scope, ["posts"]);
});

it("answers like an empty cache on a bootstrap that binds no coordinates", async () => {
  const entrypoint = new CacheEntrypoint(createExecutionContext(), {} as Env);

  expect(await entrypoint.fetchGet(scope, "abc123", ["posts"])).toBeNull();
  await entrypoint.fetchSet(scope, "abc123", entry(), ["posts"]);
  await entrypoint.revalidateTags(scope, ["posts"]);
});

it("raises the invalidation as its only record when no tag table is bound", async () => {
  const writer = raiseRecorder();
  await cacheWith({ raise: writer.raise }).revalidateTags(scope, ["posts"], { expire: 60 });

  expect(writer.raises).toEqual([{ scope, records: { posts: { stale: 5_000, expired: 65_000 } } }]);
});

it("surfaces a refused raise when the raise is the only record", async () => {
  await expect(
    cacheWith({
      raise: async () => {
        throw new Error("429");
      },
    }).revalidateTags(scope, ["posts"]),
  ).rejects.toThrow("429");
});

it("serves fetch entries with no AWS bindings at all", async () => {
  await seedEntry(entry());
  const ctx = Object.assign(createExecutionContext(), { props: { scope } });
  const entrypoint = new CacheEntrypoint(ctx, { OCEL_CACHE_STORE: env.TAG_SNAPSHOT_STORE } as Env);

  expect(await entrypoint.fetchGet(scope, "abc123", [])).toEqual(entry());
});
